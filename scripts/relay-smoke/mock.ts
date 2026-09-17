// Deterministic fake upstream: OpenAI chat/responses/embeddings/images and Anthropic messages.
const port = Number(process.env.MOCK_PORT ?? 13911)
const usage = { prompt_tokens: 11, completion_tokens: 7, total_tokens: 18 }
const sse = (events: string[]) =>
  new Response(events.map((e) => e + '\n\n').join(''), { headers: { 'content-type': 'text/event-stream' } })
const json = (body: unknown, status = 200) => Response.json(body, { status })

Bun.serve({
  port,
  hostname: '127.0.0.1',
  async fetch(req) {
    const path = new URL(req.url).pathname
    const body = req.method === 'POST' ? await req.json().catch(() => ({})) : {}
    if (body.model === 'gpt-4o') return json({ error: { message: 'mock upstream exploded', type: 'server_error' } }, 500)
    if (body.model === 'gpt-4') return json({ error: { message: 'mock says bad request', type: 'invalid_request_error' } }, 400)

    if (path.endsWith('/chat/completions')) {
      if (body.stream) {
        const chunk = (delta: object, finish: string | null = null) =>
          'data: ' + JSON.stringify({ id: 'chatcmpl-mock', object: 'chat.completion.chunk', created: 1, model: body.model, choices: [{ index: 0, delta, finish_reason: finish }] })
        return sse([
          chunk({ role: 'assistant', content: '' }),
          chunk({ content: 'Hello ' }),
          chunk({ content: 'world' }),
          chunk({}, 'stop'),
          'data: ' + JSON.stringify({ id: 'chatcmpl-mock', object: 'chat.completion.chunk', created: 1, model: body.model, choices: [], usage }),
          'data: [DONE]',
        ])
      }
      return json({ id: 'chatcmpl-mock', object: 'chat.completion', created: 1, model: body.model, choices: [{ index: 0, message: { role: 'assistant', content: 'Hello world' }, finish_reason: 'stop' }], usage })
    }
    if (path.endsWith('/responses')) {
      const response = { id: 'resp_mock', object: 'response', created_at: 1, status: 'completed', model: body.model, output: [{ id: 'msg_mock', type: 'message', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: 'Hello world', annotations: [] }] }], usage: { input_tokens: 11, output_tokens: 7, total_tokens: 18 } }
      if (body.stream) {
        return sse([
          'event: response.created\ndata: ' + JSON.stringify({ type: 'response.created', response: { ...response, status: 'in_progress', output: [] } }),
          'event: response.output_text.delta\ndata: ' + JSON.stringify({ type: 'response.output_text.delta', item_id: 'msg_mock', output_index: 0, content_index: 0, delta: 'Hello world' }),
          'event: response.completed\ndata: ' + JSON.stringify({ type: 'response.completed', response }),
        ])
      }
      return json(response)
    }
    if (path.endsWith('/embeddings'))
      return json({ object: 'list', model: body.model, data: [{ object: 'embedding', index: 0, embedding: [0.1, 0.2, 0.3] }], usage: { prompt_tokens: 5, total_tokens: 5 } })
    if (path.endsWith('/images/generations'))
      return json({ created: 1, data: [{ url: 'https://mock.invalid/image.png', revised_prompt: 'a cat' }] })
    if (path.endsWith('/messages')) {
      if (body.stream) {
        const ev = (type: string, data: object) => `event: ${type}\ndata: ` + JSON.stringify({ type, ...data })
        return sse([
          ev('message_start', { message: { id: 'msg_mock', type: 'message', role: 'assistant', model: body.model, content: [], stop_reason: null, usage: { input_tokens: 11, output_tokens: 1 } } }),
          ev('content_block_start', { index: 0, content_block: { type: 'text', text: '' } }),
          ev('content_block_delta', { index: 0, delta: { type: 'text_delta', text: 'Hello world' } }),
          ev('content_block_stop', { index: 0 }),
          ev('message_delta', { delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 7 } }),
          ev('message_stop', {}),
        ])
      }
      return json({ id: 'msg_mock', type: 'message', role: 'assistant', model: body.model, content: [{ type: 'text', text: 'Hello world' }], stop_reason: 'end_turn', stop_sequence: null, usage: { input_tokens: 11, output_tokens: 7 } })
    }
    return json({ error: { message: 'mock: no route ' + path } }, 404)
  },
})
console.log('mock upstream on ' + port)
