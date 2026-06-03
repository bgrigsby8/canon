package main

import (
	"canon"
	camera "go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
)

func main() {
	// ModularMain can take multiple APIModel arguments, if your module implements multiple models.
	module.ModularMain(resource.APIModel{API: camera.API, Model: canon.Camera})
}
