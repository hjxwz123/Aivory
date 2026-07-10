package llm

// Built-in document-generation skill (§4.5.1 "quality watershed").
//
// These recipes are what separates an editorial-quality PDF/PPTX/DOCX from
// improvised output. They used to be inlined into every system prompt whenever
// python_execute was enabled — ~800 tokens of dead weight on every turn that
// never produces a document. They now ship as a built-in skill: models that
// can call use_skill get a one-line index entry and load the full text on
// demand (§4.17 progressive disclosure); prompt/none-mode models still get the
// text inlined by composeSystemPrompt.
//
// Served from code, not the skills table, so it can't be deleted in the admin
// panel; an admin-defined skill with the same name intentionally shadows it,
// both in the system-prompt index and in useSkillTool's lookup.

// DocGenSkillName is the reserved name advertised in the skills index.
const DocGenSkillName = "document-generation"

// DocGenWhen is the one-line "when to use" entry for the skills index.
const DocGenWhen = "MUST load this BEFORE generating any downloadable document (PDF / PPTX / DOCX / XLSX) — it contains the required recipes and self-checks."

const docGenFence = "```"

// DocGenRecipes is the full skill text, also used as the inline fallback.
const DocGenRecipes = `## Document-generation recipes (run inside python_execute, write to /workspace/outputs/)

**PDF (preferred):** semantic HTML (h1/h2/p/ul/table/blockquote — not styled divs) + WeasyPrint; it handles page breaks, fonts, and tables natively.
` + docGenFence + `python
from weasyprint import HTML, CSS
HTML(string=html).write_pdf("/workspace/outputs/report.pdf", stylesheets=[CSS(string="""
@page { size: A4; margin: 25mm; }
body { font-family: 'Noto Sans CJK SC','DejaVu Sans'; font-size: 11pt; line-height: 1.55; color: #1f2937; }
h1 { font-size: 22pt; } h2 { font-size: 15pt; margin: 18pt 0 6pt; } h1, h2 { color: #0f172a; font-weight: 600; }
table { width: 100%; border-collapse: collapse; }
th, td { border: 1px solid #e2e8f0; padding: 6pt 8pt; text-align: left; } th { background: #f1f5f9; font-weight: 600; }
""")])
` + docGenFence + `

**PPT (.pptx):** author each slide as a semantic-HTML string and parse it into native PPTX shapes with BeautifulSoup + python-pptx — the sandbox has NO browser, never attempt playwright/screenshot routes.
` + docGenFence + `python
from bs4 import BeautifulSoup
from pptx import Presentation
from pptx.util import Inches, Pt
from pptx.dml.color import RGBColor
prs = Presentation(); prs.slide_width, prs.slide_height = Inches(13.33), Inches(7.5)
for html in slides_html:  # one string per slide, e.g. "<h1>Title</h1><p>Subtitle</p>"
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    tf = slide.shapes.add_textbox(Inches(0.8), Inches(0.6), Inches(11.7), Inches(6)).text_frame
    tf.word_wrap = True
    for el in BeautifulSoup(html, "html.parser").find_all(["h1", "h2", "p", "li", "img"]):
        if el.name == "img" and el.get("src"):
            slide.shapes.add_picture(el["src"], Inches(1), Inches(2.2), width=Inches(8)); continue
        p = tf.add_paragraph() if tf.paragraphs[0].runs else tf.paragraphs[0]
        r = p.add_run(); r.text = ("• " if el.name == "li" else "") + el.get_text()
        r.font.name = "Noto Sans CJK SC"; r.font.bold = el.name in ("h1", "h2")
        r.font.size = Pt({"h1": 40, "h2": 28}.get(el.name, 18))
        r.font.color.rgb = RGBColor.from_string("0f172a" if r.font.bold else "1f2937")
prs.save("/workspace/outputs/deck.pptx")
` + docGenFence + `
Map <table> via slide.shapes.add_table likewise. Charts/diagrams: render a matplotlib PNG, then add_picture. Web images: fetch_image(url) downloads to /workspace/uploads/ and returns a local path (no direct internet). User uploads are already there.

**Word (.docx):** python-docx — set doc.styles['Normal'].font.name = 'Noto Sans CJK SC' (size Pt(11)), then add_heading / add_paragraph / add_table / add_picture.

**Excel (.xlsx):** openpyxl or xlsxwriter (charts, conditional formatting, frozen panes all supported).

**Self-check before presenting (no browser — never screenshots):** reopen each file structurally: os.path.getsize > 0; pypdf — page count + page-1 extract_text() shows expected content incl. CJK glyphs; python-pptx — slide count + title/bullet text present; DOCX/XLSX likewise. Always set Noto Sans CJK fonts so Chinese never renders as tofu boxes (□□□). On failure, fix and re-render (up to 3 attempts).`
