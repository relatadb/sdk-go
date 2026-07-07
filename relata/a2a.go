package relata

import "context"

// A2AClient is the synchronous Agent-to-Agent task client. It wraps task
// submission/polling, LangGraph checkpoint load/save, and the agent card.
type A2AClient struct {
	c *Client
}

// NewA2AClient constructs an A2AClient that inherits the parent client's auth
// and tenant context. Mirrors the Python reference's A2AClient.from_client.
func NewA2AClient(c *Client) *A2AClient {
	return &A2AClient{c: c}
}

// SubmitTask submits a long-running A2A task. The payload matches the A2A
// protocol — typically {"name": "...", "input": {...}, "skill": ...}. Returns
// the task record with its id and initial status.
func (a *A2AClient) SubmitTask(ctx context.Context, payload map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.postJSON(ctx, "/a2a/tasks", payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetTask polls a task's status. Returns id, status, result, error, …
func (a *A2AClient) GetTask(ctx context.Context, taskID string) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.get(ctx, "/a2a/tasks/"+taskID, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListCheckpoints lists the LangGraph checkpoints for a thread.
func (a *A2AClient) ListCheckpoints(ctx context.Context, threadID string) ([]map[string]any, error) {
	var resp map[string]any
	if err := a.c.get(ctx, "/a2a/checkpoints/"+threadID, &resp); err != nil {
		return nil, err
	}
	return unwrapList(resp, "checkpoints"), nil
}

// LoadCheckpoint loads a specific checkpoint by id.
func (a *A2AClient) LoadCheckpoint(ctx context.Context, threadID, checkpointID string) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.get(ctx, "/a2a/checkpoints/"+threadID+"/"+checkpointID, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SaveCheckpoint saves (PUT) a checkpoint. The payload matches the LangGraph
// checkpointer shape ({"checkpoint": ..., "metadata": ...}). Returns the
// server's save receipt.
func (a *A2AClient) SaveCheckpoint(ctx context.Context, threadID, checkpointID string, payload map[string]any) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.putJSON(ctx, "/a2a/checkpoints/"+threadID+"/"+checkpointID, payload, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// AgentCard fetches the A2A agent card (GET /.well-known/agent.json) which
// advertises the server's A2A capabilities, skills, and auth modes.
func (a *A2AClient) AgentCard(ctx context.Context) (map[string]any, error) {
	var resp map[string]any
	if err := a.c.get(ctx, "/.well-known/agent.json", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
