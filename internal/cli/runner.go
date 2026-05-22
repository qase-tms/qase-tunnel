package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/qase-tms/qase-tunnel/internal/transport/frpc"
)

type FrpcRunner struct {
	Binary  string
	Output  io.Writer
	Spawner frpc.Spawner
}

func (r *FrpcRunner) Run(ctx context.Context, inputs frpc.Inputs) error {
	tomlPath, cleanup, err := frpc.WriteTemp(inputs)
	if err != nil {
		return fmt.Errorf("write frpc config: %w", err)
	}
	defer cleanup()

	lc := frpc.New(frpc.Options{
		Binary:     r.Binary,
		ConfigPath: tomlPath,
		Spawner:    r.Spawner,
		Output:     frpc.FuncWriter(func(line string) { fmt.Fprintln(r.Output, line) }),
		Inputs:     inputs,
	})

	return lc.Run(ctx)
}
