package oasis

import "fmt"

type ErrLLM struct {
	Provider string
	Message  string
}

func (e *ErrLLM) Error() string {
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

type ErrHTTP struct {
	Status int
	Body   string
}

func (e *ErrHTTP) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Body)
}
