/// <reference lib="webworker" />

import readXlsxFile from 'read-excel-file/web-worker'

const MAX_RENDERED_ROWS = 250
const MAX_RENDERED_COLUMNS = 50

type CellValue = string | number | boolean | Date | null

interface ParseRequest {
  data: ArrayBuffer
}

interface ParsedSheet {
  sheet: string
  data: CellValue[][]
  totalRows: number
  totalColumns: number
}

type ParseResponse =
  | { ok: true; sheets: ParsedSheet[] }
  | { ok: false; message: string }

const worker = self as unknown as DedicatedWorkerGlobalScope

worker.onmessage = (event: MessageEvent<ParseRequest>) => {
  void (async () => {
    try {
      const workbook = await readXlsxFile(event.data.data)
      const sheets = workbook.map((sheet) => {
        const totalRows = sheet.data.length
        const totalColumns = sheet.data.reduce((max, row) => Math.max(max, row.length), 0)
        return {
          sheet: sheet.sheet,
          totalRows,
          totalColumns,
          data: sheet.data
            .slice(0, MAX_RENDERED_ROWS)
            .map((row) => row.slice(0, MAX_RENDERED_COLUMNS)) as CellValue[][],
        }
      })
      worker.postMessage({ ok: true, sheets } satisfies ParseResponse)
    } catch (error) {
      worker.postMessage({
        ok: false,
        message: error instanceof Error ? error.message : String(error),
      } satisfies ParseResponse)
    }
  })()
}

export {}
