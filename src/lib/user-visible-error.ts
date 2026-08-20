const canceledError = /\b(?:context|request|operation) cancel(?:ed|led)\b/i
const timeoutError = /\b(?:context deadline exceeded|deadline exceeded|timed?\s*out)\b/i

// This is defense in depth for messages persisted by older servers. New tool
// errors are sanitized before they are streamed or stored. Only error fields
// pass through this detector, so successful tool output and citations may still
// contain normal URLs and file paths.
const internalDiagnosticPatterns = [
  /\b(?:get|post|put|patch|delete|head)\s+"?(?:https?|wss?):\/\//i,
  /(?:https?|wss?):\/\/(?:localhost|127\.0\.0\.1|\[?::1\]?|(?:\d{1,3}\.){3}\d{1,3})(?::\d+)?/i,
  /\b(?:authorization|proxy-authorization|bearer|api[_ -]?key|access[_ -]?token|client[_ -]?secret)\b/i,
  /\b(?:dial tcp|connection refused|no such host|tls handshake|x509:|unexpected eof|failed to resolve reference)\b/i,
  /(?:^|[\s"'(])(?:\/Users\/|\/home\/|\/var\/|\/etc\/|[A-Za-z]:\\)[^\s"')]+/,
  /(?:https?|wss?):\/\/\S+[^\s.,;:!?)](?:\s+|$).{0,80}\b(?:failed|failure|error|request|connect|upstream)\b/i,
]

export const TOOL_FAILURE_MESSAGE = 'Tool execution failed. Please try again.'
export const OPERATION_CANCELED_MESSAGE = 'The operation was canceled.'
export const TOOL_TIMEOUT_MESSAGE = 'The tool timed out. Please try again.'
export const GENERAL_FAILURE_MESSAGE = 'Something went wrong. Please try again.'

export function sanitizeUserVisibleError(
  value: string | null | undefined,
  fallback = GENERAL_FAILURE_MESSAGE,
): string {
  const message = value?.trim() ?? ''
  if (!message) return fallback
  if (canceledError.test(message)) return OPERATION_CANCELED_MESSAGE
  if (timeoutError.test(message)) return TOOL_TIMEOUT_MESSAGE
  if (internalDiagnosticPatterns.some((pattern) => pattern.test(message))) return fallback
  return message
}

export function sanitizeToolErrorOutput(value: string | null | undefined): string {
  return sanitizeUserVisibleError(value, TOOL_FAILURE_MESSAGE)
}
