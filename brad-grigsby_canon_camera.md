# Model brad-grigsby:canon:camera

A Viam `camera` component that controls a Canon camera over Canon's
[Camera Control API (CCAPI)](https://developercommunity.usa.canon.com/s/article/Introduction-to-Camera-Control-API-CCAPI).

The camera's **live view** is exposed through the standard `GetImages` method, so it
works with the data manager, the control tab, and vision services. Full-resolution
stills and other camera controls are available through `DoCommand`.

> Live view returns a JPEG of the camera's current view. It does not write to the SD
> card or actuate the shutter. Use the `capture` DoCommand for full-resolution stills.

## Requirements

- A Canon camera with CCAPI enabled and reachable over the network. CCAPI must be
  activated on the camera (Canon provides it on request for supported models) and the
  camera connected to the same network as the machine running this module.

## Configuration

```json
{
  "ip_address": "10.1.2.105",
  "use_https": true,
  "port": "443",
  "live_view_size": "medium"
}
```

### Attributes

| Name             | Type   | Inclusion | Description                                                                                 |
|------------------|--------|-----------|---------------------------------------------------------------------------------------------|
| `ip_address`     | string | Required  | IP address the camera exposes its CCAPI server on.                                          |
| `use_https`      | bool   | Optional  | Connect over HTTPS. Required if the camera shows an `https://...` URL. Defaults to `false`. |
| `port`           | string | Optional  | CCAPI server port. Defaults to `443` when `use_https` is set, otherwise `8080`.             |
| `live_view_size` | string | Optional  | Live view resolution: `small` or `medium`. Defaults to `medium`.                            |

> **HTTPS / self-signed certificate:** CCAPI's HTTPS mode uses a self-signed certificate. When
> `use_https` is enabled, the module connects without verifying it. A camera showing
> `https://10.1.2.105:443/ccapi` needs `use_https: true`.

### Example Configuration

For a camera offering `https://10.1.2.105:443/ccapi`:

```json
{
  "ip_address": "10.1.2.105",
  "use_https": true
}
```

## DoCommand

Any combination of the following commands may be sent in a single `DoCommand` call;
each result is keyed by its command name.

### capture

Fire the shutter for a full-resolution still, wait for the camera to save it, then
download it. Returns the CCAPI path, mime type, and base64-encoded image bytes.

```json
{ "capture": { "af": true } }
```

Response:

```json
{
  "capture": {
    "path": "http://192.168.1.142:8080/ccapi/ver100/contents/sd/100CANON/IMG_0327.JPG",
    "mime_type": "image/jpeg",
    "image_base64": "<base64 jpeg>"
  }
}
```

### list_contents

List every file URL across the camera's storage devices.

```json
{ "list_contents": true }
```

### device_info

Return the camera's device information (model, firmware, serial, etc.).

```json
{ "device_info": true }
```

### get_settings / get_setting / set_setting

Read all shooting settings, read a single setting, or change one. Setting names follow
CCAPI (e.g. `av` for aperture, `tv` for shutter speed, `iso`).

```json
{ "get_settings": true }
```

```json
{ "get_setting": "av" }
```

```json
{ "set_setting": { "setting": "av", "value": "f4.0" } }
```
