# Module canon

Control a Canon camera from Viam over Canon's
[Camera Control API (CCAPI)](https://developercommunity.usa.canon.com/s/article/Introduction-to-Camera-Control-API-CCAPI).

The camera's **live view** is served through the standard camera API (`GetImages`), so it
works out of the box with the Viam data manager, the control tab preview, and vision
services. Full-resolution stills, SD-card listing, device info, and shooting settings are
available through `DoCommand`.

## Requirements

- A Canon camera with CCAPI enabled. CCAPI must be activated on the camera (Canon enables
  it on request for supported models) and the camera must be reachable over the network
  from the machine running this module.

## Models

This module provides the following model(s):

- [`brad-grigsby:canon:camera`](brad-grigsby_canon_camera.md) — a `camera` component that
  streams a Canon camera's live view and exposes capture and camera controls via `DoCommand`.

## Configuration

```json
{
  "ip_address": "192.168.1.142",
  "port": "8080",
  "live_view_size": "medium"
}
```

| Name             | Type   | Inclusion | Description                                                      |
|------------------|--------|-----------|------------------------------------------------------------------|
| `ip_address`     | string | Required  | IP address the camera exposes its CCAPI server on.               |
| `port`           | string | Optional  | CCAPI server port. Defaults to `8080`.                           |
| `live_view_size` | string | Optional  | Live view resolution: `small` or `medium`. Defaults to `medium`. |

See [the model documentation](brad-grigsby_canon_camera.md) for the full list of
`DoCommand` actions (`capture`, `list_contents`, `device_info`, and shooting settings).
