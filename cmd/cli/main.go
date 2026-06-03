package main

import (
	"canon"
	"context"
	camera "go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
)

func main() {
	err := realMain()
	if err != nil {
		panic(err)
	}
}

func realMain() error {
	ctx := context.Background()
	logger := logging.NewLogger("cli")

	deps := resource.Dependencies{}
	// can load these from a remote machine if you need

	cfg := canon.Config{}

	thing, err := canon.NewCamera(ctx, deps, camera.Named("foo"), &cfg, logger)
	if err != nil {
		return err
	}
	defer thing.Close(ctx)

	return nil
}
