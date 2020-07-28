package main

import (
	"os"
	"sort"
	"time"

	gelf "github.com/seatgeek/logrus-gelf-formatter"
	"github.com/seatgeek/resec/resec/reconciler"
	log "github.com/sirupsen/logrus"
	cli "gopkg.in/urfave/cli.v1"
)

var Version = "local-dev"

func main() {
	app := cli.NewApp()
	app.Name = "resec"
	app.Usage = "Redis cluster manager"
	app.Version = Version

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "announce-addr",
			Usage:  "IP:Port of Redis to be announced to Consul",
			EnvVar: "ANNOUNCE_ADDR",
		},
		cli.DurationFlag{
			Name:   "consul-deregister-service-after",
			Usage:  "Specifies that checks associated with a service should deregister after this time. If a check is in the critical state for more than this configured value, then its associated service (and all of its associated checks) will automatically be deregistered",
			Value:  72 * time.Hour,
			EnvVar: "CONSUL_DEREGISTER_SERVICE_AFTER",
		},
		cli.StringFlag{
			Name:   "consul-lock-key",
			Usage:  "KV lock location, should be overriden if multiple instances running in the same consul DC",
			Value:  "resec/.lock",
			EnvVar: "CONSUL_LOCK_KEY",
		},
		cli.IntFlag{
			Name:   "consul-lock-monitor-retries",
			Usage:  "Number of retries of lock receives 500 Error from Consul",
			Value:  3,
			EnvVar: "CONSUL_LOCK_MONITOR_RETRIES",
		},
		cli.DurationFlag{
			Name:   "consul-lock-monitor-retry-interval",
			Usage:  "Retry interval if lock receives 500 Error from Consul",
			Value:  time.Second,
			EnvVar: "CONSUL_LOCK_MONITOR_RETRY_INTERVAL",
		},
		cli.StringFlag{
			Name:   "consul-lock-session-name",
			Usage:  "Lock session Name to distinguish multiple resec masters on one host",
			Value:  "resec",
			EnvVar: "CONSUL_LOCK_SESSION_NAME",
		},
		cli.DurationFlag{
			Name:   "consul-lock-ttl",
			Value:  15 * time.Second,
			EnvVar: "CONSUL_LOCK_TTL",
		},
		cli.StringFlag{
			Name:   "consul-service-name",
			Usage:  "Consul service name for tag based service discovery",
			EnvVar: "CONSUL_SERVICE_NAME",
		},
		cli.StringFlag{
			Name:   "consul-service-prefix",
			Usage:  "Name Prefix, will be followed by '-[master|slave]', ignored if CONSUL_SERVICE_NAME is used",
			Value:  "redis",
			EnvVar: "CONSUL_SERVICE_PREFIX",
		},
		cli.StringFlag{
			Name:   "consul-master-tags",
			Usage:  "Comma separated list of tags to be added to master instance. The first tag (index 0) is used to configure the role of the Redis/resec task, and must be different from index 0 in SLAVE_TAGS",
			EnvVar: "MASTER_TAGS",
		},
		cli.StringFlag{
			Name:   "consul-slave-tags",
			Usage:  "Comma separated list of tags to be added to slave instance. The first tag (index 0) is used to configure the role of the Redis/resec task, and must be different from index 0 in MASTER_TAGS",
			EnvVar: "SLAVE_TAGS",
		},
		cli.DurationFlag{
			Name:   "healthcheck-interval",
			Value:  5 * time.Second,
			EnvVar: "HEALTHCHECK_INTERVAL",
		},
		cli.DurationFlag{
			Name:   "healthcheck-timeout",
			Value:  2 * time.Second,
			EnvVar: "HEALTHCHECK_TIMEOUT",
		},
		cli.StringFlag{
			Name:   "log-level",
			Value:  "info",
			Usage:  "Log level (debug, info, warn/warning, error, fatal, panic)",
			EnvVar: "LOG_LEVEL",
		},
		cli.StringFlag{
			Name:   "log-format",
			Value:  "text",
			Usage:  "Log format (text, gelf, json)",
			EnvVar: "LOG_FORMAT",
		},
		cli.StringFlag{
			Name:   "redis-addr",
			Value:  "127.0.0.1:6379",
			Usage:  "IP + Port for the Redis server",
			EnvVar: "REDIS_ADDR",
		},
		cli.StringFlag{
			Name:   "redis-password",
			Usage:  "Password for the Redis server",
			EnvVar: "REDIS_PASSWORD",
		},
	}
	app.Before = func(c *cli.Context) error {
		level, err := log.ParseLevel(c.String("log-level"))
		if err != nil {
			return err
		}
		log.SetLevel(level)

		switch c.String("log-format") {
		case "text":
			log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
		case "json":
			log.SetFormatter(&log.JSONFormatter{})
		case "gelf":
			log.SetFormatter(&gelf.GelfFormatter{})
		default:
			log.Fatal("Invalid log format (text, json, gelf)")
		}
		return nil
	}
	app.Action = func(c *cli.Context) error {
		r, err := reconciler.NewReconciler(c)
		if err != nil {
			return err
		}

		r.Run()
		return nil
	}

	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(app.Commands))

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
