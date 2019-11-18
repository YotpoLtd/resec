# Changelog

## 0.6.0 (???)

- Upgrade Go to 1.13
- Use Go `mod` instead of `dep`
- Add `SIGUSR1` and `SIGUSR2` signal that will dump internal state for debugging purposes
- Add `LOG_LEVEL=debug` state diff output
- Fixed a gazillion typos of `reconciler`
- Split up the `eval` and `apply` in the reconciler for easier management
- Improve Redis and Consul manager logic and state management
- Make Redis connection handler do backoff like Consul
- Various code formatting improvements
- Build macOS and Linux binary on release

## 0.5.1 (January 16, 2019)

FIXES:

- Docker build

## 0.5.0 (September 16, 2018)

FEATURES:

- Configure via CLI arguments
- Add json/gelf log support
- Tests

FIXES:

- Split-brain when redis master restarted without restart of its resec

## 0.4.3 (July 15, 2018)

FIXES:

- Allow same Master and Slave tags when using CONSUL_SERVICE_PREFIX

## 0.4.2 (July 15, 2018)

FEATURES:

- Verbose Health Check names giving the option to distinguish checks of multiple Resec instances running on the same node

## 0.4.1 (May 16, 2018)

FEATURES:

- Prevent master and slave tag having index 0 identical when using CONSUL_SERVICE_NAME
- Test!

## 0.4.0 (April 26, 2018)

FEATURES:

- Service check now registered as initially passing to avoid service flapping

FIXES:

- Handling slave session invalidation
- Prevent from shutting down if local consul agent is not healthy

## 0.3.0 (April 23, 2018)

FEATURES:

- Consul lock session name can be adjusted
- Consul lock retry option added
- Dependencies updated (Consul to 1.0.7, go-redis to 6.10.2)

## 0.2.0 (March 13, 2018)

FEATURES:

- Consul service name for tag based service discovery
- Option to ADD (MASTER|SLAVE)_TAGS to be populated to consul
- Managing dependencies using [dep](https://github.com/golang/dep)
- Go version bumped to 0.10.x

## 0.1.0 (January 22, 2018)

- Initial release

FEATURES:

- Redis Health monitoring
- Consul Agent outage handling
- Looks working =)
