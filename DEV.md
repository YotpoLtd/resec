# Local development

This document outline have I usually develop / debug Resec locally. It assume Linux/MacOS, but appending ".exe" to most binaries should make it Windows compatible out of the box.

## Prerequisites

* Need Consul installed (`brew install consul`)
* Need Redis installed (`brew install redis`)
* Need Go 1.13 installed (`brew install go`)

## Getting started

In the directory where the source code is checked out, run the following commands:

* `go get`
* `go install`

## Terminal window layout

I usually have an iTerm layout like below for all the relevant programs.

Having the windows in the same tab makes it easy to kill / restart / debug issues as you can see all "cluster" activity.

### Command setting up the terminal layout in iTerm 2

* Crate new colum: `CMD + d`
* Create new row in colum 2: `CMD + shift + d`
* Create new row in colum 2: `CMD + shift + d`
* Click in the left window (colum 1)
* Create new row in colum 1: `CMD + shift + d`
* Create new row in colum 1: `CMD + shift + d`

### Commands for the individual windows

All the terminal windows should have their current directory (CWD) set to the directory with the Resec source code.

* Redis 1: `$ redis-server --port 6666`
* Redis 2: `$ redis-server --port 7777`
* Consul : `$ consul agent -bind 127.0.0.1 -server -bootstrap-expect 1 -data-dir /tmp/consul-test -ui -node resec-local`
* Resec 1: `$ resec --consul-master-tags master --consul-service-name redis --consul-slave-tags slave --redis-addr 127.0.0.1:6666`
* Resec 2: `$ resec --consul-master-tags master --consul-service-name redis --consul-slave-tags slave --redis-addr 127.0.0.1:7777`

```text
                                iTerm 2
+----------------------------------+---------------------------------+
|                                  |                                 |
|                                  |                                 |
|             Resec 1              |             Redis 1             |
|                                  |                                 |
|                                  |                                 |
+--------------------------------------------------------------------+
|                                  |                                 |
|                                  |                                 |
|             Resec 2              |             Redis 2             |
|                                  |                                 |
|                                  |                                 |
+--------------------------------------------------------------------+
|                                  |                                 |
|                                  |                                 |
|          Interactive             |             Consul              |
|                                  |                                 |
|                                  |                                 |
+----------------------------------+---------------------------------+
```

For Consul, I don't use `consul agent -dev` because it does not persist state between runs, which is very useful to test recovery logic in Resec. With `-dev` the cluster would always be empty if you kill Consul, so locking timeouts etc would not work as in real world. That being said, I *also* test in `-dev` mode when doing major work, since a clean slate can also cause interesting races in the code, and better emulate a new Resec cluster being created.

## Workflow

You can access the [local Consul UI on http://localhost:8500/ui/](http://localhost:8500/ui/dc1/services) to debug / inspect whats going on.

From here on out, I usally edit the code and in the `interactive` shell run `go install` and then stop/start Resec/Redis/Consul depending on what behaviour I want to test. I very often clear the individual windows between runs with `CMD + k`.

The easiest way to get an overview of the Resec state is the [Local Consul node service list](http://localhost:8500/ui/dc1/nodes/resec-local) (click the `services` tab). Also of interest is the `Lock Sessions` tab on the same page.

From here on out, when I make code changes I run `go install` in the `interactive` window and then stop/start the processes in the Resec windows.

If there are any window with interesting content, you can focus on the window and hit `cmd + shift + enter` to make the window temporary full screen. Hit `cmd + shift + enter` again to undo the full screen.

Good luck!
