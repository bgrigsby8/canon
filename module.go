// Package canon implements a Viam camera component that controls a Canon
// camera over Canon's Camera Control API (CCAPI).
//
// The camera's live view is exposed through the standard camera Images method,
// so it integrates with the Viam data manager, the control tab, and computer
// vision services. Higher-level actions that don't fit the streaming model
// (full-resolution capture, listing the SD card, reading device info, and
// reading/writing shooting settings) are exposed through DoCommand.
package canon

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	camera "go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/data"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/spatialmath"
	rutils "go.viam.com/rdk/utils"
)

// Camera is the model triple for this component.
var Camera = resource.NewModel("brad-grigsby", "canon", "camera")

// errUnimplemented is returned by camera methods that don't apply to a Canon DSLR/mirrorless camera.
var errUnimplemented = errors.New("unimplemented")

const (
	defaultHTTPPort  = "8080"
	defaultHTTPSPort = "443"

	// ccapiBase is the version-prefixed root of all CCAPI endpoints used here.
	ccapiBase = "/ccapi/ver100"

	liveViewPath      = ccapiBase + "/shooting/liveview"
	liveViewFlipPath  = ccapiBase + "/shooting/liveview/flip"
	shutterButtonPath = ccapiBase + "/shooting/control/shutterbutton"
	eventPollingPath  = ccapiBase + "/event/polling?continue=off"
	deviceInfoPath    = ccapiBase + "/deviceinformation"
	contentsPath      = ccapiBase + "/contents"
	settingsPath      = ccapiBase + "/shooting/settings"

	// captureTimeout bounds how long we wait for the camera to write a still to its card after the shutter fires.
	captureTimeout = 15 * time.Second
)

func init() {
	resource.RegisterComponent(camera.API, Camera,
		resource.Registration[camera.Camera, *Config]{
			Constructor: newCanonCamera,
		},
	)
}

// Config is the JSON configuration for a Canon CCAPI camera.
type Config struct {
	// IPAddress is the IP the camera exposes its CCAPI server on. Required.
	IPAddress string `json:"ip_address"`
	// Port is the CCAPI server port. Defaults to 443 when use_https is set, otherwise 8080.
	Port string `json:"port,omitempty"`
	// UseHTTPS connects over HTTPS. CCAPI's HTTPS mode uses a self-signed certificate, so
	// enabling this also disables TLS certificate verification. Cameras that present a URL
	// like "https://<ip>:443/ccapi" require this.
	UseHTTPS bool `json:"use_https,omitempty"`
	// LiveViewSize is the live view resolution requested from the camera: "small" or "medium".
	// Defaults to "medium" when omitted.
	LiveViewSize string `json:"live_view_size,omitempty"`
}

// scheme returns the URL scheme for the configured transport.
func (cfg *Config) scheme() string {
	if cfg.UseHTTPS {
		return "https"
	}
	return "http"
}

// port returns the configured CCAPI port or the scheme-appropriate default.
func (cfg *Config) port() string {
	if cfg.Port != "" {
		return cfg.Port
	}
	if cfg.UseHTTPS {
		return defaultHTTPSPort
	}
	return defaultHTTPPort
}

// liveViewSize returns the configured live view size or the default.
func (cfg *Config) liveViewSize() string {
	if cfg.LiveViewSize == "" {
		return "medium"
	}
	return cfg.LiveViewSize
}

// Validate ensures the config is usable and reports dependencies (this component has none).
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if cfg.IPAddress == "" {
		return nil, nil, fmt.Errorf("%s: 'ip_address' is required", path)
	}
	switch cfg.LiveViewSize {
	case "", "small", "medium":
	default:
		return nil, nil, fmt.Errorf("%s: 'live_view_size' must be \"small\" or \"medium\"", path)
	}
	return nil, nil, nil
}

type canonCamera struct {
	resource.Named
	resource.AlwaysRebuild

	logger logging.Logger
	cfg    *Config

	baseURL    string
	httpClient *http.Client

	cancelCtx  context.Context
	cancelFunc func()
	activeBkgd sync.WaitGroup

	// mu guards the mutable session state below.
	mu sync.Mutex
	// liveViewStarted tracks whether we've already asked the camera to begin live view.
	liveViewStarted bool
	// connected reflects whether the background loop currently reaches the camera's CCAPI server.
	connected bool
	// lastErr is the most recent connection error, surfaced by the status command ("" when connected).
	lastErr string
}

// contentList is the shape CCAPI uses to return lists of storage devices, directories, and files.
type contentList struct {
	Path []string `json:"path"`
}

func newCanonCamera(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (camera.Camera, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}
	return NewCamera(ctx, deps, rawConf.ResourceName(), conf, logger)
}

// NewCamera constructs a Canon CCAPI camera. It returns immediately without blocking on the camera:
// a background loop establishes and maintains the CCAPI session (see connectLoop), so a camera that
// is unreachable or asleep at startup doesn't fail construction and is picked up once it's available.
func NewCamera(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *Config, logger logging.Logger) (camera.Camera, error) {
	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	// CCAPI is an embedded HTTP server that drops idle connections after a few seconds. Reusing a
	// pooled keep-alive connection therefore hangs the next request until it times out, so we open a
	// fresh connection per request.
	transport := &http.Transport{DisableKeepAlives: true}
	if conf.UseHTTPS {
		// CCAPI's HTTPS mode presents a self-signed certificate, so verification must be skipped.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // CCAPI uses a self-signed certificate
		logger.Warn("connecting to CCAPI over HTTPS with TLS certificate verification disabled (self-signed certificate)")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	c := &canonCamera{
		Named:      name.AsNamed(),
		logger:     logger,
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
		baseURL:    fmt.Sprintf("%s://%s:%s", conf.scheme(), conf.IPAddress, conf.port()),
		httpClient: httpClient,
	}

	c.activeBkgd.Add(1)
	go c.connectLoop()

	return c, nil
}

// --- connection management ---

const (
	// connectRetryInterval is how long to wait between connection attempts while the camera is unreachable.
	connectRetryInterval = 5 * time.Second
	// heartbeatInterval is how often to re-probe the camera once connected, to detect a camera that has
	// dropped off (power save, sleep, etc.).
	heartbeatInterval = 45 * time.Second
	// probeTimeout bounds a single reachability probe so a hung or unreachable camera is reflected in
	// the connection state within seconds rather than waiting on the full client timeout.
	probeTimeout = 8 * time.Second
)

// connectLoop runs in the background for the lifetime of the component. It probes the camera until
// CCAPI responds (the first probe is what prompts the on-camera approval), then heartbeats so a
// camera that drops off (power save, sleep, weak Wi-Fi) is detected and reconnected when it returns.
// Live view is started lazily by Images rather than held open here.
func (c *canonCamera) connectLoop() {
	defer c.activeBkgd.Done()
	for {
		err := c.probe(c.cancelCtx)
		c.updateConnection(err)

		wait := connectRetryInterval
		if err == nil {
			wait = heartbeatInterval
		}
		select {
		case <-c.cancelCtx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// probe makes a single bounded reachability request to the camera. The first successful probe is also
// what dismisses the camera's "waiting to connect" prompt (after on-camera approval).
func (c *canonCamera) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return c.getJSON(ctx, deviceInfoPath, nil)
}

// updateConnection records the result of a probe, surfacing the last error for the status command and
// logging connection state transitions so they're visible without debug logging. Losing the connection
// clears liveViewStarted so live view is restarted on the next Images call once the camera is back.
func (c *canonCamera) updateConnection(err error) {
	c.mu.Lock()
	was := c.connected
	c.connected = err == nil
	if err != nil {
		c.lastErr = err.Error()
		c.liveViewStarted = false
	} else {
		c.lastErr = ""
	}
	c.mu.Unlock()

	switch {
	case err == nil && !was:
		c.logger.Infof("connected to Canon camera at %s", c.baseURL)
	case err != nil && was:
		c.logger.Warnf("lost connection to Canon camera at %s: %v", c.baseURL, err)
	case err != nil:
		c.logger.Debugf("camera not reachable, retrying in %s: %v", connectRetryInterval, err)
	}
}

// isConnected reports whether the background loop currently reaches the camera's CCAPI server.
func (c *canonCamera) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// lastError returns the most recent connection error, or "" if the last probe succeeded.
func (c *canonCamera) lastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// --- low-level CCAPI helpers ---

// resolveURL turns a CCAPI path into an absolute URL. Event-polling results are already absolute
// (e.g. "http://host:port/ccapi/...") while paths we construct are relative to baseURL.
func (c *canonCamera) resolveURL(p string) string {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	return c.baseURL + p
}

// doRequest performs an HTTP request against the camera, marshaling body as JSON when non-nil and
// translating any >=400 status into an error. The caller owns the returned response body.
func (c *canonCamera) doRequest(ctx context.Context, method, p string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.resolveURL(p), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("CCAPI %s %s failed (%d): %s", method, p, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// getJSON performs a GET and decodes the JSON response into out (out may be nil to discard it).
func (c *canonCamera) getJSON(ctx context.Context, p string, out any) error {
	resp, err := c.doRequest(ctx, http.MethodGet, p, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sendJSON performs a POST or PUT with a JSON body and discards the response.
func (c *canonCamera) sendJSON(ctx context.Context, method, p string, body any) error {
	resp, err := c.doRequest(ctx, method, p, body)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// getBytes performs a GET and returns the raw response body, e.g. for a JPEG frame or image download.
func (c *canonCamera) getBytes(ctx context.Context, p string) ([]byte, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, p, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// --- live view ---

// ensureLiveView starts the camera's live view stream once. Subsequent calls are no-ops until the
// stream is marked stopped (on a flip failure or on Close).
func (c *canonCamera) ensureLiveView(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.liveViewStarted {
		return nil
	}
	body := map[string]string{
		"liveviewsize":  c.cfg.liveViewSize(),
		"cameradisplay": "on",
	}
	if err := c.sendJSON(ctx, http.MethodPost, liveViewPath, body); err != nil {
		return fmt.Errorf("failed to start live view: %w", err)
	}
	c.liveViewStarted = true
	return nil
}

// liveViewFrame returns the most recent live view JPEG, starting (or restarting) live view as needed.
func (c *canonCamera) liveViewFrame(ctx context.Context) ([]byte, error) {
	if err := c.ensureLiveView(ctx); err != nil {
		return nil, err
	}

	frame, err := c.getBytes(ctx, liveViewFlipPath)
	if err != nil {
		// The camera may have torn down live view (e.g. after a mode change). Restart once and retry.
		c.mu.Lock()
		c.liveViewStarted = false
		c.mu.Unlock()
		if startErr := c.ensureLiveView(ctx); startErr != nil {
			return nil, err
		}
		if frame, err = c.getBytes(ctx, liveViewFlipPath); err != nil {
			return nil, fmt.Errorf("failed to read live view frame: %w", err)
		}
	}
	if len(frame) == 0 {
		return nil, errors.New("camera returned an empty live view frame")
	}
	return frame, nil
}

// --- still capture & content access ---

// capture fires the shutter for a full-resolution still, waits for it to land on the card, and
// downloads it. It returns the image bytes and the CCAPI path of the captured file.
func (c *canonCamera) capture(ctx context.Context, autofocus bool) (imageBytes []byte, fileURL string, err error) {
	// Drain any pending events so the addedcontents we observe belongs to this capture.
	_ = c.getJSON(ctx, eventPollingPath, nil)

	if err := c.sendJSON(ctx, http.MethodPost, shutterButtonPath, map[string]bool{"af": autofocus}); err != nil {
		return nil, "", fmt.Errorf("failed to trigger shutter: %w", err)
	}

	fileURL, err = c.pollForNewFile(ctx, captureTimeout)
	if err != nil {
		return nil, "", err
	}

	imageBytes, err = c.getBytes(ctx, fileURL)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download captured image %q: %w", fileURL, err)
	}
	return imageBytes, fileURL, nil
}

// pollForNewFile polls the CCAPI event endpoint until the camera reports a newly added file or the
// timeout elapses, returning the URL of the most recent added file.
func (c *canonCamera) pollForNewFile(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		var event struct {
			AddedContents []string `json:"addedcontents"`
		}
		if err := c.getJSON(ctx, eventPollingPath, &event); err != nil {
			return "", fmt.Errorf("failed to poll for captured image: %w", err)
		}
		if n := len(event.AddedContents); n > 0 {
			return event.AddedContents[n-1], nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for camera to save the captured image")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// listContents walks the camera's storage devices and directories and returns every file URL it finds.
func (c *canonCamera) listContents(ctx context.Context) ([]string, error) {
	var storages contentList
	if err := c.getJSON(ctx, contentsPath, &storages); err != nil {
		return nil, fmt.Errorf("failed to list storage devices: %w", err)
	}

	var files []string
	for _, storage := range storages.Path {
		var dirs contentList
		if err := c.getJSON(ctx, storage, &dirs); err != nil {
			return nil, fmt.Errorf("failed to list directories in %q: %w", storage, err)
		}
		for _, dir := range dirs.Path {
			var contents contentList
			if err := c.getJSON(ctx, dir, &contents); err != nil {
				return nil, fmt.Errorf("failed to list contents in %q: %w", dir, err)
			}
			files = append(files, contents.Path...)
		}
	}
	return files, nil
}

// --- camera.Camera interface ---

// Images returns the camera's current live view frame as a single JPEG NamedImage.
func (c *canonCamera) Images(
	ctx context.Context, filterSourceNames []string, extra map[string]any,
) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	sourceName := c.Name().Name
	if len(filterSourceNames) > 0 && !slices.Contains(filterSourceNames, sourceName) {
		return nil, resource.ResponseMetadata{}, nil
	}

	frame, err := c.liveViewFrame(ctx)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	named, err := camera.NamedImageFromBytes(frame, sourceName, rutils.MimeTypeJPEG, data.Annotations{})
	if err != nil {
		return nil, resource.ResponseMetadata{}, fmt.Errorf("failed to construct named image: %w", err)
	}
	return []camera.NamedImage{named}, resource.ResponseMetadata{CapturedAt: time.Now()}, nil
}

// Properties reports that this is a color (JPEG) camera with no point cloud support.
func (c *canonCamera) Properties(ctx context.Context) (camera.Properties, error) {
	return camera.Properties{
		SupportsPCD: false,
		ImageType:   camera.ColorStream,
		MimeTypes:   []string{rutils.MimeTypeJPEG},
	}, nil
}

// NextPointCloud is not supported; a Canon camera does not produce point clouds.
func (c *canonCamera) NextPointCloud(ctx context.Context, extra map[string]any) (pointcloud.PointCloud, error) {
	return nil, errUnimplemented
}

// Geometries returns no geometries; the camera has no associated spatial geometry.
func (c *canonCamera) Geometries(ctx context.Context, extra map[string]any) ([]spatialmath.Geometry, error) {
	return nil, nil
}

// DoCommand exposes higher-level CCAPI actions that don't map onto the streaming Images path.
//
// Supported commands (any combination may be sent in one call):
//
//	{"capture": {"af": true}}                              -> {"path", "mime_type", "image_base64"}
//	{"list_contents": true}                                -> ["<file url>", ...]
//	{"device_info": true}                                  -> {camera device information}
//	{"get_settings": true}                                 -> {all shooting settings}
//	{"get_setting": "av"}                                  -> {value, ability} for one setting
//	{"set_setting": {"setting": "av", "value": "f4.0"}}    -> "ok"
//	{"status": true}                                       -> {"connected": bool, "base_url": "..."}
func (c *canonCamera) DoCommand(ctx context.Context, cmd map[string]any) (map[string]any, error) {
	resp := map[string]any{}

	if _, ok := cmd["status"]; ok {
		status := map[string]any{
			"connected": c.isConnected(),
			"base_url":  c.baseURL,
		}
		if lastErr := c.lastError(); lastErr != "" {
			status["last_error"] = lastErr
		}
		resp["status"] = status
	}

	if raw, ok := cmd["capture"]; ok {
		autofocus := false
		if opts, ok := raw.(map[string]any); ok {
			if af, ok := opts["af"].(bool); ok {
				autofocus = af
			}
		}
		imageBytes, fileURL, err := c.capture(ctx, autofocus)
		if err != nil {
			return nil, err
		}
		resp["capture"] = map[string]any{
			"path":         fileURL,
			"mime_type":    rutils.MimeTypeJPEG,
			"image_base64": base64.StdEncoding.EncodeToString(imageBytes),
		}
	}

	if _, ok := cmd["list_contents"]; ok {
		files, err := c.listContents(ctx)
		if err != nil {
			return nil, err
		}
		resp["list_contents"] = files
	}

	if _, ok := cmd["device_info"]; ok {
		var info map[string]any
		if err := c.getJSON(ctx, deviceInfoPath, &info); err != nil {
			return nil, fmt.Errorf("failed to get device info: %w", err)
		}
		resp["device_info"] = info
	}

	if _, ok := cmd["get_settings"]; ok {
		var settings map[string]any
		if err := c.getJSON(ctx, settingsPath, &settings); err != nil {
			return nil, fmt.Errorf("failed to get shooting settings: %w", err)
		}
		resp["get_settings"] = settings
	}

	if raw, ok := cmd["get_setting"]; ok {
		name, ok := raw.(string)
		if !ok || name == "" {
			return nil, errors.New("get_setting requires a setting name string, e.g. \"av\"")
		}
		var setting map[string]any
		if err := c.getJSON(ctx, settingsPath+"/"+name, &setting); err != nil {
			return nil, fmt.Errorf("failed to get setting %q: %w", name, err)
		}
		resp["get_setting"] = setting
	}

	if raw, ok := cmd["set_setting"]; ok {
		opts, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("set_setting requires an object with 'setting' and 'value'")
		}
		name, ok := opts["setting"].(string)
		if !ok || name == "" {
			return nil, errors.New("set_setting requires a 'setting' name string, e.g. \"av\"")
		}
		value, ok := opts["value"]
		if !ok {
			return nil, errors.New("set_setting requires a 'value'")
		}
		if err := c.sendJSON(ctx, http.MethodPut, settingsPath+"/"+name, map[string]any{"value": value}); err != nil {
			return nil, fmt.Errorf("failed to set setting %q: %w", name, err)
		}
		resp["set_setting"] = "ok"
	}

	if len(resp) == 0 {
		return nil, errors.New("no recognized command; supported: capture, list_contents, device_info, get_settings, get_setting, set_setting, status")
	}
	return resp, nil
}

// Close stops the background connection loop and best-effort stops the camera's live view.
func (c *canonCamera) Close(ctx context.Context) error {
	c.cancelFunc()
	c.activeBkgd.Wait()

	c.mu.Lock()
	started := c.liveViewStarted
	c.liveViewStarted = false
	c.mu.Unlock()

	if started {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		body := map[string]string{"liveviewsize": "off", "cameradisplay": "keep"}
		if err := c.sendJSON(stopCtx, http.MethodPost, liveViewPath, body); err != nil {
			c.logger.Warnf("failed to stop live view on close: %v", err)
		}
	}
	return nil
}
