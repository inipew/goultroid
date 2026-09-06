package core

// ResponseOptions controls the behavior of a normal command response.
type ResponseOptions struct {
	// DeleteTrigger removes the incoming command message after the response
	// has been successfully sent. Deletion is best-effort.
	DeleteTrigger bool
}

// Respond sends a response and optionally cleans up the incoming command.
// The response is sent first so cleanup can never hide a successful response.
func (c *Context) Respond(text string, opts ResponseOptions) error {
	if err := c.Messages.Reply(text); err != nil {
		return err
	}
	if opts.DeleteTrigger {
		_ = c.Messages.Delete()
	}
	return nil
}

// ReplyAndDelete is the convenience API for the common userbot command flow.
func (c *Context) ReplyAndDelete(text string) error {
	return c.Respond(text, ResponseOptions{DeleteTrigger: true})
}
