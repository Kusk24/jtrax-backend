"""Generate the one-page OCR setup sheet as a PDF.

A script rather than a checked-in binary so the sheet cannot drift from the
code: the env var names and the endpoint path are written once, here, next to
the thing they configure.

Run:  python docs/make_ocr_setup_pdf.py
"""

import pathlib
import sys

from reportlab.lib import colors
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle
from reportlab.lib.units import mm
from reportlab.pdfgen import canvas
from reportlab.platypus import Paragraph

OUT = pathlib.Path(__file__).parent / "JTrax - OCR setup.pdf"

INK = colors.HexColor("#24417C")
BODY = colors.HexColor("#355575")
MUTED = colors.HexColor("#6B7C93")
RULE = colors.HexColor("#D8E2F0")
CODE_BG = colors.HexColor("#F2F6FC")
WARN = colors.HexColor("#9D4343")

W, H = A4
M = 17 * mm


def main() -> int:
    c = canvas.Canvas(str(OUT), pagesize=A4)
    c.setTitle("JTrax — Registration form OCR setup")
    y = H - M

    c.setFillColor(INK)
    c.setFont("Helvetica-Bold", 17)
    c.drawString(M, y, "JTrax — Registration form scanning (OCR)")
    y -= 6.5 * mm
    c.setFont("Helvetica", 9.5)
    c.setFillColor(MUTED)
    c.drawString(M, y, "Photograph a paper registration form; the console pre-fills the wizard. Staff confirm before anything saves.")
    y -= 4 * mm
    c.setStrokeColor(RULE)
    c.setLineWidth(0.9)
    c.line(M, y, W - M, y)
    y -= 8 * mm

    def heading(text, gap=5.4 * mm, space_above=4.2 * mm):
        nonlocal y
        # Space above, or a heading sits on the paragraph that precedes it.
        y -= space_above
        c.setFillColor(INK)
        c.setFont("Helvetica-Bold", 11)
        c.drawString(M, y, text)
        y -= gap

    def body(text, size=9.3, leading=4.6 * mm, colour=BODY):
        nonlocal y
        style = ParagraphStyle("b", fontName="Helvetica", fontSize=size,
                               leading=leading, textColor=colour)
        p = Paragraph(text, style)
        _, h = p.wrap(W - 2 * M, 60 * mm)
        p.drawOn(c, M, y - h)
        y -= h + 2.2 * mm

    def code(lines, pad=2.6 * mm):
        nonlocal y
        h = pad * 2 + len(lines) * 4.5 * mm
        c.setFillColor(CODE_BG)
        c.setStrokeColor(RULE)
        c.roundRect(M, y - h, W - 2 * M, h, 2 * mm, stroke=1, fill=1)
        c.setFont("Courier", 8.6)
        c.setFillColor(INK)
        ty = y - pad - 3.3 * mm
        for ln in lines:
            c.drawString(M + 3.5 * mm, ty, ln)
            ty -= 4.5 * mm
        y -= h + 4.5 * mm

    # ---- 1. get a key ----------------------------------------------------
    heading("1. Get an API key", space_above=0)
    body("Go to <b>aistudio.google.com/apikey</b>, sign in, and click <b>Create API key</b>. "
         "Copy it once — it is not shown again.")
    body("<b>Which tier:</b> the free tier needs no credit card, but Google may use what you send it to "
         "improve its products and train models. What you send is a photograph of a child's name, address, "
         "date of birth and phone number. <b>Use a paid key for real registrations</b> — same code, same "
         "endpoint, different terms. The free key is fine for trying it out with a form you made up.",
         colour=WARN)

    # ---- 2. configure ----------------------------------------------------
    heading("2. Put it in the backend environment")
    body("Three variables on <b>jtrax-backend</b>. Never commit them; on Render they go in "
         "Environment &rarr; Add Environment Variable.")
    code([
        "OCR_PROVIDER = gemini",
        "OCR_API_KEY  = <the key you just copied>",
        "OCR_MODEL    = gemini-2.5-flash        # optional, this is the default",
    ])
    body("Restart the service. With no key set, scanning is simply switched off: the console says so, "
         "and the rest of JTrax is unaffected.")

    # ---- 3. use ----------------------------------------------------------
    heading("3. Use it")
    body("Console &rarr; <b>Students</b> &rarr; <b>Register Student</b> &rarr; <b>Upload Registration Form</b> &rarr; "
         "choose a photo (JPEG, PNG, WebP or HEIC, up to 10&nbsp;MB). The wizard opens pre-filled.")
    body("Fields it fills: name, date of birth, chess level, current school, FIDE ID and rating, and the "
         "email and contact number (onto the guardian). Gender, address, how they heard of JCA, previous "
         "chess school, course package and the ticked courses are <b>shown but not saved</b> — JTrax has "
         "nowhere to put them yet.")

    # ---- 4. checks -------------------------------------------------------
    heading("4. What to expect")
    body("&bull; <b>Nothing is saved by the scan.</b> It only fills the form; a person still presses Register.<br/>"
         "&bull; Anything the model was unsure of is listed under <b>“Please check”</b> — look at those before saving.<br/>"
         "&bull; A date it cannot read is left blank rather than guessed.<br/>"
         "&bull; The photo is not stored: it is read and dropped. The paper form remains the record.<br/>"
         "&bull; Only <b>Admin</b> and <b>Reception</b> can scan.")

    # ---- 5. troubleshooting ---------------------------------------------
    heading("5. If it does not work")
    body("<b>“Form scanning is not configured”</b> &mdash; OCR_PROVIDER or OCR_API_KEY is missing, or the "
         "service was not restarted.<br/>"
         "<b>“Could not read that photo”</b> &mdash; usually a real problem with the photo: shoot the whole "
         "page straight on, in even light, no shadow across the writing. It can also mean the key is wrong "
         "or the free-tier daily quota is spent.<br/>"
         "<b>Wrong or missing values</b> &mdash; expected on messy handwriting. Type over them; that is what "
         "the form is for.")

    # ---- footer ----------------------------------------------------------
    y -= 1 * mm
    c.setStrokeColor(RULE)
    c.line(M, y, W - M, y)
    y -= 4.6 * mm
    c.setFont("Helvetica", 7.9)
    c.setFillColor(MUTED)
    c.drawString(M, y, "Endpoint: POST /api/v1/registrations/scan  ·  staff only  ·  rate-limited  ·  writes nothing")
    y -= 3.9 * mm
    c.drawString(M, y, "Swapping provider: implement ocr.Provider in jtrax-backend/internal/ocr and switch OCR_PROVIDER. No calling code changes.")

    c.showPage()
    c.save()
    print(f"wrote {OUT}  ({OUT.stat().st_size / 1024:.0f} KB)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
