"""Generate REAL .xlsx / .pptx fixtures with the reference libraries.

The hand-written XML in the unit tests proves the surgical writer is
structurally correct; these fixtures prove something the unit tests cannot:
that a file produced by the writer is accepted by an independent, real
implementation (openpyxl / python-pptx) with its formatting intact.

Run:  python tests/fixtures/ooxml/make_fixtures.py
"""

from __future__ import annotations

import pathlib

from openpyxl import Workbook
from openpyxl.styles import Alignment, Font, PatternFill
from openpyxl.utils import get_column_letter
from docx import Document
from docx.shared import Pt
from pptx import Presentation
from pptx.util import Inches, Pt as PptxPt

HERE = pathlib.Path(__file__).resolve().parent


def make_xlsx(path: pathlib.Path) -> None:
    book = Workbook()

    # --- Sheet 1: a formatted report with a formula and a merged title --------
    sheet = book.active
    sheet.title = "Report"

    title = sheet.cell(row=1, column=1, value="Quarterly Report")
    title.font = Font(bold=True, size=14)
    sheet.merge_cells("A1:C1")
    title.alignment = Alignment(horizontal="center")

    headers = ["Item", "Units", "Revenue"]
    for column, label in enumerate(headers, start=1):
        cell = sheet.cell(row=2, column=column, value=label)
        cell.font = Font(bold=True, color="FFFFFF")
        cell.fill = PatternFill("solid", fgColor="B1552F")

    rows = [("Widgets", 120, 2400.0), ("Gadgets", 80, 4000.0), ("Gizmos", 15, 750.0)]
    for offset, (name, units, revenue) in enumerate(rows):
        row = 3 + offset
        sheet.cell(row=row, column=1, value=name)
        sheet.cell(row=row, column=2, value=units)
        money = sheet.cell(row=row, column=3, value=revenue)
        money.number_format = '#,##0.00'

    total = sheet.cell(row=6, column=2, value="=SUM(B3:B5)")
    total.font = Font(bold=True)
    sheet.cell(row=6, column=3, value="=SUM(C3:C5)").number_format = '#,##0.00'

    sheet.column_dimensions["A"].width = 24
    sheet.freeze_panes = "A3"

    # --- Sheet 2: shared strings and a boolean -------------------------------
    second = book.create_sheet("Notes")
    second["A1"] = "Shared string alpha"
    second["A2"] = "Shared string beta"
    second["B1"] = True
    second["B2"] = 0.125
    second["B2"].number_format = '0.0%'

    book.save(path)


def make_pptx(path: pathlib.Path) -> None:
    deck = Presentation()

    slide = deck.slides.add_slide(deck.slide_layouts[0])
    slide.shapes.title.text = "Quarterly Review"
    slide.placeholders[1].text = "Prepared by the data team"

    slide2 = deck.slides.add_slide(deck.slide_layouts[1])
    slide2.shapes.title.text = "Highlights"
    body = slide2.placeholders[1].text_frame
    body.text = "Revenue up 12%"
    body.add_paragraph().text = "Churn down to 1.4%"

    deck.save(path)


def make_docx(path: pathlib.Path) -> None:
    document = Document()

    document.add_heading("Quarterly Report", level=1)
    document.add_paragraph("Revenue is up 12% this quarter, and churn fell to 1.4%.")

    table = document.add_table(rows=2, cols=2)
    table.cell(0, 0).text = "Item"
    table.cell(0, 1).text = "Units"
    table.cell(1, 0).text = "Widgets"
    table.cell(1, 1).text = "120"

    document.add_heading("Outlook", level=2)
    paragraph = document.add_paragraph()
    run = paragraph.add_run("Steady growth expected.")
    run.bold = True
    run.font.size = Pt(12)

    document.save(path)


def main() -> None:
    xlsx = HERE / "sample.xlsx"
    pptx = HERE / "sample.pptx"
    docx = HERE / "sample.docx"
    make_xlsx(xlsx)
    make_pptx(pptx)
    make_docx(docx)
    for path in (xlsx, pptx, docx):
        print(f"wrote {path.name} ({path.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
