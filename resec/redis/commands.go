package redis

import "github.com/YotpoLtd/resec/resec/state"

const (
	StartCommand       = CommandName("start")
	StopCommand        = CommandName("stop")
	RunAsSlaveCommand  = CommandName("run_as_slave")
	RunAsMasterCommand = CommandName("run_as_master")
)

type CommandName string

type Command struct {
	name        CommandName
	consulState state.Consul
}

func (c *Command) Name() string {
	return string(c.name)
}

func NewCommand(cmd CommandName, consulState state.Consul) Command {
	return Command{
		name:        cmd,
		consulState: consulState,
	}
}
