package redis

import "github.com/YotpoLtd/resec/resec/state"

const (
	StartCommand       = commandName("start")
	StopCommand        = commandName("stop")
	RunAsSlaveCommand  = commandName("run_as_slave")
	RunAsMasterCommand = commandName("run_as_master")
)

type commandName string

type Command struct {
	name        commandName
	consulState state.Consul
}

func NewCommand(cmd commandName, consulState state.Consul) Command {
	return Command{
		name:        cmd,
		consulState: consulState,
	}
}
