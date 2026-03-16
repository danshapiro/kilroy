package llm

import (
	"fmt"
	"sync"
)

type EnvAdapterFactory func() (adapter ProviderAdapter, configured bool, err error)

var (
	envFactoriesMu sync.Mutex
	envFactories   []EnvAdapterFactory
)

// RegisterEnvAdapterFactory registers a factory that can construct a ProviderAdapter
// from environment variables. Provider packages should call this from init().
func RegisterEnvAdapterFactory(factory EnvAdapterFactory) {
	if factory == nil {
		return
	}
	envFactoriesMu.Lock()
	envFactories = append(envFactories, factory)
	envFactoriesMu.Unlock()
}

// NewFromEnv constructs a Client by registering any provider adapters that can be
// constructed from environment variables. The first successfully registered provider
// becomes the default provider.
//
// Note: providers are discovered via factories registered with RegisterEnvAdapterFactory.
func NewFromEnv() (*Client, error) {
	envFactoriesMu.Lock()
	factories := append([]EnvAdapterFactory{}, envFactories...)
	envFactoriesMu.Unlock()

	c := NewClient()
	for _, f := range factories {
		a, ok, err := f()
		if err != nil {
			return nil, err
		}
		if ok && a != nil {
			c.Register(a)
		}
	}
	if len(c.ProviderNames()) == 0 {
		return nil, fmt.Errorf("no LLM providers configured via environment variables")
	}
	return c, nil
}

var (
	defaultClientMu   sync.Mutex
	defaultClientInit bool
	defaultClient     *Client
	defaultClientErr  error
)

// SetDefaultClient sets the module-level default client and disables lazy env initialization.
func SetDefaultClient(c *Client) {
	defaultClientMu.Lock()
	defer defaultClientMu.Unlock()
	defaultClient = c
	defaultClientErr = nil
	defaultClientInit = true
}

// DefaultClient returns the module-level default client, lazily constructing it
// from environment variables on first use.
func DefaultClient() (*Client, error) {
	defaultClientMu.Lock()
	if defaultClientInit {
		c, err := defaultClient, defaultClientErr
		defaultClientMu.Unlock()
		return c, err
	}
	defaultClientMu.Unlock()

	c, err := NewFromEnv()

	defaultClientMu.Lock()
	defaultClient = c
	defaultClientErr = err
	defaultClientInit = true
	defaultClientMu.Unlock()
	return c, err
}
