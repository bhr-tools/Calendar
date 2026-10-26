"""End-to-end smoke test of the web app in Chromium (desktop and phone).

Run against a fresh instance (see tests/e2e/run.sh). Screenshots are written
to $OUT. The browser clock is frozen on Monday 26 October 2026, 09:30 (Paris).
"""
import os
import re
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

from playwright.sync_api import sync_playwright, expect

BASE = os.environ.get("BASE_URL", "http://localhost:18080")
OUT = os.environ.get("OUT", "/tmp")
FEED = os.environ.get("FEED_URL", "")
PASSWORD = "correct horse"
NOW = datetime(2026, 10, 26, 9, 30, tzinfo=ZoneInfo("Europe/Paris"))
errors = []


def watch(page):
    page.on("console", lambda m: m.type in ("error", "warning") and "401" not in m.text
            and errors.append(f"console {m.type}: {m.text}"))
    page.on("pageerror", lambda e: errors.append(f"page error: {e}"))


def shot(page, name):
    page.wait_for_timeout(250)
    page.screenshot(path=f"{OUT}/{name}.png")


def column(page, day_index):
    return page.locator(".day-col").nth(day_index)


def slot_point(page, day_index, minutes):
    """Viewport coordinates of a time in a day column of the week grid."""
    col = column(page, day_index).bounding_box()
    hour = page.evaluate("parseFloat(getComputedStyle(document.querySelector('.grid')).getPropertyValue('--hour'))")
    return col["x"] + col["width"] / 2, col["y"] + minutes / 60 * hour


def drag(page, start, end, ctrl=False):
    page.mouse.move(*start)
    page.mouse.down()
    if ctrl:
        page.keyboard.down("Control")
    steps = 8
    for i in range(1, steps + 1):
        page.mouse.move(start[0] + (end[0] - start[0]) * i / steps, start[1] + (end[1] - start[1]) * i / steps)
    page.mouse.up()
    if ctrl:
        page.keyboard.up("Control")
    page.wait_for_timeout(400)


def event(page, title):
    return page.locator(".event:not(.ghost)", has_text=title)


def desktop(browser):
    ctx = browser.new_context(viewport={"width": 1400, "height": 900}, timezone_id="Europe/Paris", locale="en-GB",
                              color_scheme="dark")
    page = ctx.new_page()
    page.clock.install(time=NOW)
    watch(page)
    page.goto(BASE)

    # First run: choose the password.
    page.get_by_placeholder("At least 4 characters").fill(PASSWORD)
    page.get_by_placeholder("Repeat the password").fill(PASSWORD)
    page.get_by_role("button", name="Set password and continue").click()
    expect(page.locator(".grid")).to_be_visible()
    expect(page.locator("#view-title")).to_contain_text("Oct – Nov 2026")
    expect(page.locator(".day-head.today")).to_contain_text("26")
    shot(page, "01-week-empty")

    # Draw an event on Tuesday 10:00 – 11:30 and name it.
    page.locator(".grid-body").evaluate("el => el.scrollTop = 0")
    drag(page, slot_point(page, 1, 10 * 60 + 5), slot_point(page, 1, 11 * 60 + 30))
    expect(page.locator(".draft-form")).to_be_visible()
    expect(page.locator(".draft-when")).to_contain_text("10:00 – 11:30")
    page.keyboard.type("Deep work")
    page.keyboard.press("Enter")
    expect(event(page, "Deep work")).to_have_count(1)
    expect(event(page, "Deep work")).to_contain_text("10:00 – 11:30")
    shot(page, "02-created")

    # Click opens the editor; rename and save with Ctrl+Enter.
    event(page, "Deep work").click()
    expect(page.locator(".modal.editor")).to_be_visible()
    page.locator(".editor-title").fill("Deep work: calendar")
    page.locator(".editor-row .input[placeholder=Location]").fill("Home")
    page.keyboard.press("Control+Enter")
    expect(page.locator(".modal.editor")).to_have_count(0)
    expect(event(page, "Deep work: calendar")).to_have_count(1)

    # Ctrl+drag to Wednesday copies it; a plain drag moves it.
    src = event(page, "Deep work: calendar").bounding_box()
    start = (src["x"] + src["width"] / 2, src["y"] + 10)
    target = slot_point(page, 2, 14 * 60)
    drag(page, start, (target[0], target[1] + 10), ctrl=True)
    expect(event(page, "Deep work: calendar")).to_have_count(2)
    copy = column(page, 2).locator(".event", has_text="Deep work")
    expect(copy).to_contain_text("14:00 – 15:30")
    shot(page, "03-copied")

    box = copy.bounding_box()
    to = slot_point(page, 3, 16 * 60)
    drag(page, (box["x"] + box["width"] / 2, box["y"] + 10), (to[0], to[1] + 10))
    moved = column(page, 3).locator(".event", has_text="Deep work")
    expect(moved).to_contain_text("16:00 – 17:30")
    expect(column(page, 2).locator(".event")).to_have_count(0)

    # Resize from the bottom edge to 18:00.
    box = moved.bounding_box()
    end = slot_point(page, 3, 18 * 60)
    drag(page, (box["x"] + box["width"] / 2, box["y"] + box["height"] - 2), end)
    expect(column(page, 3).locator(".event", has_text="Deep work")).to_contain_text("16:00 – 18:00")

    # Keyboard: select, move, copy / paste, delete and undo.
    page.locator("body").click(position={"x": 5, "y": 5})
    page.keyboard.press("Escape")
    page.keyboard.press("j")
    expect(page.locator(".event.selected")).to_have_count(1)
    page.keyboard.press("Shift+J")
    page.keyboard.press("Shift+J")
    expect(page.locator(".event.selected")).to_contain_text("10:30 – 12:00")
    page.keyboard.press("Shift+L")
    expect(column(page, 2).locator(".event.selected")).to_have_count(1)
    page.keyboard.press("y")
    page.keyboard.press("l")  # next week
    expect(page.locator("#view-title")).to_contain_text("November")
    page.keyboard.press("p")
    expect(page.locator(".event", has_text="Deep work")).to_have_count(1)
    page.keyboard.press("x")
    expect(page.locator(".event", has_text="Deep work")).to_have_count(0)
    page.get_by_role("button", name="Undo").click()
    expect(page.locator(".event", has_text="Deep work")).to_have_count(1)
    page.keyboard.press("h")
    page.wait_for_timeout(300)

    # Quick add.
    page.keyboard.press("q")
    page.keyboard.type("Sport demain 18h-19h30 @Gym")
    expect(page.locator(".token-chip.date")).to_contain_text("18:00 – 19:30")
    page.keyboard.press("Enter")
    expect(event(page, "Sport")).to_contain_text("18:00 – 19:30")

    # A recurring event from the editor (on today, Monday), then move one
    # occurrence only.
    page.keyboard.press("t")
    page.keyboard.press("a")
    page.locator(".editor-title").fill("Standup")
    page.locator(".editor-times input[type=time]").first.fill("09:00")
    page.locator(".editor-times input[type=time]").nth(1).fill("09:15")
    page.locator(".modal.editor select").first.select_option(label="Every weekday (Mon – Fri)")
    page.get_by_role("button", name="Create").click()
    expect(event(page, "Standup")).to_have_count(5)
    standup = column(page, 4).locator(".event", has_text="Standup")
    box = standup.bounding_box()
    to = slot_point(page, 4, 11 * 60)
    drag(page, (box["x"] + box["width"] / 2, box["y"] + 4), (to[0], to[1] + 4))
    page.get_by_role("button", name="This event").click()
    expect(column(page, 4).locator(".event", has_text="Standup")).to_contain_text("11:00")
    expect(column(page, 3).locator(".event", has_text="Standup")).to_contain_text("09:00")

    # All-day event from the strip.
    page.locator(".allday-cell").nth(5).click()
    page.keyboard.type("Hiking")
    page.keyboard.press("Enter")
    expect(page.locator(".bar", has_text="Hiking")).to_have_count(1)
    shot(page, "04-week-filled")

    # Subscription to an iCalendar feed (read-only).
    if FEED:
        page.locator("#add-calendar").click()
        page.get_by_role("menuitem", name="Subscribe to a feed (.ics)").click()
        page.get_by_placeholder("https://… or webcal://…").fill(FEED)
        page.get_by_placeholder("School timetable").fill("INP")
        page.get_by_role("button", name="Subscribe").click()
        expect(page.locator(".calendar-row", has_text="INP")).to_have_count(1)
        page.keyboard.press("g")
        page.keyboard.type("2026-12-08")
        page.keyboard.press("Enter")
        expect(page.locator(".event.readonly").first).to_be_visible()
        shot(page, "05-subscription")
        page.locator(".event.readonly").first.click()
        expect(page.locator(".editor-readonly").first).to_contain_text("read-only")
        page.keyboard.press("Escape")
        page.keyboard.press("t")

    # Month view, search, help, light theme.
    page.keyboard.press("m")
    expect(page.locator(".month-grid")).to_be_visible()
    expect(page.locator(".chip-title", has_text=re.compile("^Sport$"))).to_have_count(1)
    shot(page, "06-month")
    page.keyboard.press("/")
    page.keyboard.type("sport gym")
    expect(page.locator(".picker-item .picker-label", has_text=re.compile("^Sport$"))).to_have_count(1)
    page.keyboard.press("Enter")
    expect(page.locator(".chip.selected .chip-title", has_text=re.compile("^Sport$"))).to_have_count(1)
    page.keyboard.press("?")
    expect(page.locator(".modal.help")).to_be_visible()
    shot(page, "07-help")
    page.keyboard.press("Escape")
    page.emulate_media(color_scheme="light")
    page.keyboard.press("w")
    shot(page, "08-week-light")
    page.emulate_media(color_scheme="dark")

    page.locator("#connect-button").click()
    expect(page.locator(".copy-field input").nth(1)).to_have_value(re.compile(r"^http://.+/dav/$"))
    shot(page, "09-connect")
    page.keyboard.press("Escape")
    ctx.close()


def phone(browser):
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, is_mobile=True,
                              has_touch=True, timezone_id="Europe/Paris", locale="en-GB")
    page = ctx.new_page()
    page.clock.install(time=NOW)
    watch(page)
    page.goto(BASE)
    page.get_by_placeholder("Master password").fill(PASSWORD)
    page.get_by_role("button", name="Unlock").click()
    expect(page.locator(".grid")).to_be_visible()
    shot(page, "10-phone-day")
    page.locator("#fab").click()
    expect(page.locator(".quick-input")).to_be_visible()
    shot(page, "11-phone-quick-add")
    page.keyboard.press("Escape")
    page.locator("#menu-toggle").click()
    shot(page, "12-phone-sidebar")
    ctx.close()


with sync_playwright() as p:
    browser = p.chromium.launch()
    try:
        desktop(browser)
        phone(browser)
    except Exception:
        # Keep a picture of the failing step.
        for ctx in browser.contexts:
            for page in ctx.pages:
                page.screenshot(path=f"{OUT}/failure.png")
        raise
    finally:
        browser.close()

if errors:
    print("\n".join(errors))
    sys.exit(1)
print("smoke test passed")
