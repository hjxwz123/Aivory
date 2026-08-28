/** Stable machine code lifted into ApiDocument.error_code by the server when a
 *  document needs OCR (MinerU) but document parsing is not configured
 *  (server rag.ParserNotConfiguredCode / api documentErrorCode()). Keep the
 *  literal in sync with server/internal/rag/parser.go. */
export const DOCUMENT_PARSER_NOT_CONFIGURED = 'document_parser_not_configured'
