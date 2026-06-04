# Module canon

Control a Canon camera from Viam over Canon's
[Camera Control API (CCAPI)](https://developercommunity.usa.canon.com/s/article/Introduction-to-Camera-Control-API-CCAPI).

The camera's **live view** is served through the standard camera API (`GetImages`), so it
works out of the box with the Viam data manager, the control tab preview, and vision
services. Full-resolution stills, SD-card listing, device info, and shooting settings are
available through `DoCommand`.

## Requirements

- A Canon camera with CCAPI enabled and reachable over the network from the machine running
  this module. Newer bodies (e.g. the EOS R5 Mark II) have CCAPI built into the menu; some
  older bodies require a one-time activation over USB with Canon's CCAPI Activation Tool.

## Models

This module provides the following model(s):

- [`brad-grigsby:canon:camera`](brad-grigsby_canon_camera.md) — a `camera` component that
  streams a Canon camera's live view and exposes capture and camera controls via `DoCommand`.

## Configuration

Most cameras work with just the IP address (plain HTTP on port 8080):

```json
{
  "ip_address": "10.1.2.105"
}
```

| Name             | Type   | Inclusion | Description                                                                                 |
|------------------|--------|-----------|---------------------------------------------------------------------------------------------|
| `ip_address`     | string | Required  | IP address the camera exposes its CCAPI server on.                                          |
| `use_https`      | bool   | Optional  | Connect over HTTPS. Required if the camera shows an `https://...` URL. Defaults to `false`. |
| `port`           | string | Optional  | CCAPI server port. Defaults to `443` when `use_https` is set, otherwise `8080`.             |
| `live_view_size` | string | Optional  | Live view resolution: `small` or `medium`. Defaults to `medium`.                            |

> **HTTPS / self-signed certificate:** CCAPI's HTTPS mode uses a self-signed certificate. When
> `use_https` is enabled, the module connects without verifying the certificate. If your camera
> displays a URL like `https://10.1.2.105:443/ccapi`, set `use_https: true`.

See [the model documentation](brad-grigsby_canon_camera.md) for the full list of
`DoCommand` actions (`capture`, `list_contents`, `device_info`, and shooting settings).
