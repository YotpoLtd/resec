package consul

import (
	consulapi "github.com/hashicorp/consul/api"
)

// interface all consul client need to satify
type consulClient interface {
	LockOpts(opts *consulapi.LockOptions) (*consulapi.Lock, error)
	ServiceRegister(service *consulapi.AgentServiceRegistration) error
	CheckRegister(check *consulapi.AgentCheckRegistration) error
	ServiceDeregister(serviceID string) error
	UpdateTTL(checkID, output, status string) error
	ServiceHealth(service, tag string, passingOnly bool, q *consulapi.QueryOptions) ([]*consulapi.ServiceEntry, *consulapi.QueryMeta, error)
}

// live client operates on a real consul Client, talking to Consul API directly
type liveClient struct {
	client *consulapi.Client
}

func (c *liveClient) LockOpts(opts *consulapi.LockOptions) (*consulapi.Lock, error) {
	return c.client.LockOpts(opts)
}

func (c *liveClient) ServiceRegister(service *consulapi.AgentServiceRegistration) error {
	return c.client.Agent().ServiceRegister(service)
}

func (c *liveClient) CheckRegister(check *consulapi.AgentCheckRegistration) error {
	return c.client.Agent().CheckRegister(check)
}

func (c *liveClient) ServiceDeregister(serviceID string) error {
	return c.client.Agent().ServiceDeregister(serviceID)
}

func (c *liveClient) UpdateTTL(checkID, output, status string) error {
	return c.client.Agent().UpdateTTL(checkID, output, status)
}

func (c *liveClient) ServiceHealth(service, tag string, passingOnly bool, q *consulapi.QueryOptions) ([]*consulapi.ServiceEntry, *consulapi.QueryMeta, error) {
	return c.client.Health().Service(service, tag, passingOnly, q)
}
