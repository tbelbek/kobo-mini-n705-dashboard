#!/usr/bin/env python3
"""Kobo Mini / InkBox e-ink dashboard + calendar server.

Stdlib only. Serves JS-free, server-side-rendered HTML for a primitive
e-ink browser (800x600, single-touch, no JavaScript).

Dashboard (/) top to bottom:
  - one line: current weather, İstanbul (no precip) + Göteborg
  - Göteborg daily (günlük) forecast
  - Göteborg hourly (saatlik), horizontal: feels-like / precip% / condition
  - weather-warning row(s) if any (threshold heuristic)
  - upcoming calendar events (family + Swedish holidays, merged)

/calendar  list (default) | ?view=month&m=YYYY-MM | ?view=day&d=YYYY-MM-DD
Calendar pages don't auto-refresh, so scroll + chosen month stay put.
Tap a day/event -> day view with full title, location, description.

Config via env vars (no source edits):
  KOBO_ICS_URL            personal calendar ICS (comma-separated ok, secret)
  KOBO_ICS_HOLIDAYS       extra feed; default = Swedish public holidays. "" to drop.
  KOBO_LAT/LON/PLACE      glance location (default İstanbul)
  KOBO_LAT2/LON2/PLACE2   detailed location (default Göteborg)
  KOBO_TZ                 display timezone (default Europe/Istanbul)
  KOBO_ASCII=1            transliterate Turkish -> ASCII (font fallback)
  KOBO_REFRESH            weather auto-refresh seconds; 0 = off (default)
  KOBO_PORT               listen port (default 8080)

Run:  python3 server.py            (0.0.0.0:8080, plain HTTP)
      python3 server.py --selftest
"""

import os
import sys
import json
import time
import html
import urllib.request
import urllib.parse
from datetime import datetime, date, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

try:
    from zoneinfo import ZoneInfo
except ImportError:  # py < 3.9
    ZoneInfo = None

# ---------------------------------------------------------------- config
LAT = float(os.environ.get("KOBO_LAT", "41.0082"))
LON = float(os.environ.get("KOBO_LON", "28.9784"))
PLACE = os.environ.get("KOBO_PLACE", "İstanbul")
LAT2 = float(os.environ.get("KOBO_LAT2", "57.7089"))
LON2 = float(os.environ.get("KOBO_LON2", "11.9746"))
PLACE2 = os.environ.get("KOBO_PLACE2", "Göteborg")
TZNAME2 = os.environ.get("KOBO_TZ2", "Europe/Stockholm")  # Göteborg hours shown in local time
TZNAME = os.environ.get("KOBO_TZ", "Europe/Stockholm")  # footer clock + event times
ICS_URL = os.environ.get("KOBO_ICS_URL", "").strip()  # family/personal (comma-separated ok)
# Extra read-only feed: Swedish public holidays by default. Set "" to drop.
HOLIDAY_ICS = os.environ.get(
    "KOBO_ICS_HOLIDAYS",
    "https://calendar.google.com/calendar/ical/"
    "sv.swedish%23holiday%40group.v.calendar.google.com/public/basic.ics").strip()
PORT = int(os.environ.get("KOBO_PORT", "8080"))
# Fallback for e-ink browsers whose font lacks Turkish glyphs or that
# mis-decode utf-8: KOBO_ASCII=1 transliterates ş/ğ/İ/... to ASCII.
ASCII = os.environ.get("KOBO_ASCII", "") not in ("", "0", "false", "False")
# Auto-refresh seconds for the weather page; 0 = off. With the once-a-day
# wake+sleep power model the device reloads on wake, so in-page refresh just
# burns the radio/CPU — default off. Set KOBO_REFRESH=600 if left always-on.
REFRESH_SECONDS = int(os.environ.get("KOBO_REFRESH", "0"))

WEATHER_TTL = 300              # cache weather this long
ICS_TTL = 900                  # cache ICS fetch this long
LIST_DAYS = 365                # upcoming horizon (wide, so sparse calendars still show their next events)
LIST_MAX = 60                  # cap rendered rows in the full list
PREVIEW_N = 7                  # events shown on the dashboard
PREVIEW_DAYS = 3               # dashboard calendar covers today + next 2 days
FORECAST_DAYS = 2              # daily data only feeds the warning row
MONTHS_SHOWN = 1               # one month; change it with the prev/next links
CELL_EVENTS = 2                # events shown per day cell, rest become "+N"
HOURLY_N = 8                   # columns in the hourly strip
HOURLY_STEP = 1                # hours between columns

# ponytail: the no-zoneinfo fallback is fixed CET, so it's an hour off in summer.
TZ = ZoneInfo(TZNAME) if ZoneInfo else timezone(timedelta(hours=1))

WMO = {
    0: "Açık", 1: "Çoğunlukla açık", 2: "Parçalı bulutlu", 3: "Kapalı",
    45: "Sisli", 48: "Kırağılı sis",
    51: "Hafif çisenti", 53: "Çisenti", 55: "Yoğun çisenti",
    56: "Dondurucu çisenti", 57: "Yoğun dondurucu çisenti",
    61: "Hafif yağmur", 63: "Yağmur", 65: "Şiddetli yağmur",
    66: "Dondurucu yağmur", 67: "Şiddetli dondurucu yağmur",
    71: "Hafif kar", 73: "Kar", 75: "Yoğun kar", 77: "Kar taneleri",
    80: "Hafif sağanak", 81: "Sağanak", 82: "Şiddetli sağanak",
    85: "Hafif kar sağanağı", 86: "Kar sağanağı",
    95: "Gök gürültülü fırtına", 96: "Dolulu fırtına", 99: "Şiddetli dolulu fırtına",
}
# flat, 1-bit weather icons (inline SVG) — no icon font, e-ink friendly
_CLOUD_HI = '<path d="M7 14h10a4.2 4.2 0 0 0 .5-8.37 5.6 5.6 0 0 0-10.6-1.1A3.7 3.7 0 0 0 7 14z"/>'
_ICONS = {
    "sun": ('<circle cx="12" cy="12" r="5"/>'
            '<g stroke="#000" stroke-width="2" stroke-linecap="round">'
            '<line x1="12" y1="1" x2="12" y2="4"/><line x1="12" y1="20" x2="12" y2="23"/>'
            '<line x1="1" y1="12" x2="4" y2="12"/><line x1="20" y1="12" x2="23" y2="12"/>'
            '<line x1="4.2" y1="4.2" x2="6.3" y2="6.3"/><line x1="17.7" y1="17.7" x2="19.8" y2="19.8"/>'
            '<line x1="19.8" y1="4.2" x2="17.7" y2="6.3"/><line x1="6.3" y1="17.7" x2="4.2" y2="19.8"/>'
            '</g>'),
    "partly": ('<circle cx="8" cy="8" r="3.4"/>'
               '<g stroke="#000" stroke-width="1.6" stroke-linecap="round">'
               '<line x1="8" y1="1.5" x2="8" y2="3"/><line x1="1.5" y1="8" x2="3" y2="8"/>'
               '<line x1="3.6" y1="3.6" x2="4.7" y2="4.7"/><line x1="12.4" y1="3.6" x2="11.3" y2="4.7"/>'
               '</g>'
               '<path d="M9 20h8a3.6 3.6 0 0 0 .4-7.17 4.8 4.8 0 0 0-9.1-1A3.2 3.2 0 0 0 9 20z" '
               'stroke="#fff" stroke-width="1.2"/>'),
    "cloud": '<path d="M7 19h10a4.5 4.5 0 0 0 .6-8.96 6 6 0 0 0-11.3-1.2A4 4 0 0 0 7 19z"/>',
    "fog": ('<g stroke="#000" stroke-width="2.2" stroke-linecap="round">'
            '<line x1="3" y1="7" x2="21" y2="7"/><line x1="4" y1="11" x2="20" y2="11"/>'
            '<line x1="3" y1="15" x2="21" y2="15"/><line x1="6" y1="19" x2="18" y2="19"/></g>'),
    "rain": (_CLOUD_HI + '<g stroke="#000" stroke-width="2" stroke-linecap="round">'
             '<line x1="9" y1="17" x2="8" y2="21"/><line x1="13" y1="17" x2="12" y2="21"/>'
             '<line x1="17" y1="17" x2="16" y2="21"/></g>'),
    "snow": (_CLOUD_HI + '<circle cx="9" cy="18.5" r="1.3"/><circle cx="13" cy="20.5" r="1.3"/>'
             '<circle cx="16.5" cy="18.5" r="1.3"/>'),
    "storm": (_CLOUD_HI + '<polygon points="12,15 8.5,20 11,20 9.5,24 15,18.5 12.3,18.5 14,15"/>'),
}


# bold close cross for the day modal (SVG, so it doesn't depend on a font glyph)
CLOSE_X = ('<svg width="30" height="30" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">'
           '<path d="M5 5L19 19M19 5L5 19" stroke="#000" stroke-width="4" stroke-linecap="round"/></svg>')


def _wcat(code):
    if code in (0, 1):
        return "sun"
    if code == 2:
        return "partly"
    if code in (45, 48):
        return "fog"
    if code in (71, 73, 75, 77, 85, 86):
        return "snow"
    if code in (95, 96, 99):
        return "storm"
    if code in (51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82):
        return "rain"
    return "cloud"


def wicon(code, size=30):
    return (f'<svg width="{size}" height="{size}" viewBox="0 0 24 24" fill="#000" '
            f'xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle">'
            f'{_ICONS[_wcat(code)]}</svg>')
TR_DAYS = ["Pzt", "Sal", "Çar", "Per", "Cum", "Cmt", "Paz"]
TR_MONTHS = ["", "Ocak", "Şubat", "Mart", "Nisan", "Mayıs", "Haziran",
             "Temmuz", "Ağustos", "Eylül", "Ekim", "Kasım", "Aralık"]

_TR_ASCII = str.maketrans("şŞğĞıİçÇöÖüÜ", "sSgGiIcCoOuU")


def asciify(s):
    return s.translate(_TR_ASCII)


# ---------------------------------------------------------------- weather
_wcache = {}


def get_weather(lat, lon, hourly=False, daily=False, tz=TZNAME):
    key = (lat, lon, hourly, daily, tz)
    c = _wcache.get(key)
    if c and time.time() - c[0] < WEATHER_TTL:
        return c[1]
    params = {
        "latitude": lat, "longitude": lon, "timezone": tz,
        "current": "temperature_2m,apparent_temperature,relative_humidity_2m,"
                   "precipitation,weather_code,wind_speed_10m",
    }
    if hourly:
        params["hourly"] = ("temperature_2m,apparent_temperature,precipitation,"
                            "precipitation_probability,weather_code")
        params["forecast_hours"] = HOURLY_STEP * HOURLY_N
    if daily:
        params["daily"] = ("weather_code,temperature_2m_max,temperature_2m_min,"
                           "precipitation_sum,precipitation_probability_max,"
                           "wind_speed_10m_max")
        params["forecast_days"] = FORECAST_DAYS
    url = "https://api.open-meteo.com/v1/forecast?" + urllib.parse.urlencode(params)
    with urllib.request.urlopen(url, timeout=10) as r:
        data = json.load(r)
    _wcache[key] = (time.time(), data)
    return data


def weather_alerts(w):
    """Threshold heuristic -> list of warning strings for the next 2 days.

    ponytail: Open-Meteo has no official warning feed. These are derived from
    the forecast. Swap for SMHI (Sweden) / MET warnings API for authoritative
    alerts.
    """
    d = w.get("daily")
    if not d:
        return []
    out = []
    for i in range(min(2, len(d["time"]))):
        when = "bugün" if i == 0 else "yarın"
        code = int(d["weather_code"][i])
        psum = d["precipitation_sum"][i] or 0
        wind = d["wind_speed_10m_max"][i] or 0
        tmin = d["temperature_2m_min"][i]
        tmax = d["temperature_2m_max"][i]
        if code in (95, 96, 99):
            out.append(f"{when}: {WMO[code]}")
        if code in (71, 73, 75, 77, 85, 86):
            out.append(f"{when}: kar ({WMO.get(code,'kar')})")
        if psum >= 20:
            out.append(f"{when}: şiddetli yağış ({psum:g} mm)")
        if wind >= 50:
            out.append(f"{when}: kuvvetli rüzgar ({round(wind)} km/s)")
        if tmin is not None and tmin <= -5:
            out.append(f"{when}: don riski ({round(tmin)}°)")
        if tmax is not None and tmax >= 34:
            out.append(f"{when}: aşırı sıcak ({round(tmax)}°)")
    return out


# ---------------------------------------------------------------- ICS
_icache = {}


def _fetch_one(url):
    c = _icache.get(url)
    if c and time.time() - c[0] < ICS_TTL:
        return c[1]
    u = url.replace("webcal://", "https://", 1)
    with urllib.request.urlopen(u, timeout=15) as r:
        raw = r.read().decode("utf-8", "replace")
    _icache[url] = (time.time(), raw)
    return raw


def _sources():
    srcs = [(u.strip(), "cal") for u in ICS_URL.replace("\n", ",").split(",") if u.strip()]
    if HOLIDAY_ICS:
        srcs.append((HOLIDAY_ICS, "holiday"))
    return srcs


def get_events(win_lo, win_hi):
    """Merge every configured feed into one sorted event list.

    Returns None if no feed is configured. A feed that fails to fetch/parse is
    skipped so one broken calendar can't blank the others.
    """
    srcs = _sources()
    if not srcs:
        return None
    out = []
    for url, tag in srcs:
        try:
            for e in parse_events(_fetch_one(url), win_lo, win_hi):
                if tag == "holiday":
                    # Google's SE feed: "Allmän helgdag" = official red day,
                    # plain "Helgdag" = observance (Fars dag, advent, ...).
                    tag_e = "red" if "Allmän helgdag" in e["description"] else "day"
                else:
                    tag_e = tag
                e["tag"] = tag_e
                out.append(e)
        except Exception:
            continue
    out.sort(key=lambda e: (e["start"] if isinstance(e["start"], datetime)
                            else datetime(e["start"].year, e["start"].month,
                                          e["start"].day, tzinfo=TZ)))
    return out


def _unfold(raw):
    out = []
    for line in raw.splitlines():
        if line[:1] in (" ", "\t") and out:
            out[-1] += line[1:]
        else:
            out.append(line)
    return out


def _unescape(v):
    return (v.replace("\\n", "\n").replace("\\N", "\n")
             .replace("\\,", ",").replace("\\;", ";").replace("\\\\", "\\"))


def _parse_dt(value, params):
    v = value.strip()
    if params.get("VALUE") == "DATE" or (len(v) == 8 and v.isdigit()):
        return date(int(v[0:4]), int(v[4:6]), int(v[6:8])), True
    is_utc = v.endswith("Z")
    v2 = v[:-1] if is_utc else v
    try:
        dt = datetime.strptime(v2, "%Y%m%dT%H%M%S")
    except ValueError:
        dt = datetime.strptime(v2, "%Y%m%dT%H%M")
    if is_utc:
        dt = dt.replace(tzinfo=timezone.utc)
    else:
        tzid = params.get("TZID")
        zi = None
        if tzid and ZoneInfo:
            try:
                zi = ZoneInfo(tzid)
            except Exception:
                zi = None
        dt = dt.replace(tzinfo=zi or TZ)
    return dt.astimezone(TZ), False


def _parse_rrule(s):
    d = {}
    for part in s.split(";"):
        if "=" in part:
            k, val = part.split("=", 1)
            d[k.upper()] = val
    return d


def _add_months(d, n):
    m = d.month - 1 + n
    y = d.year + m // 12
    m = m % 12 + 1
    for day in range(d.day, 27, -1):
        try:
            return d.replace(year=y, month=m, day=day)
        except ValueError:
            continue
    return d.replace(year=y, month=m, day=min(d.day, 28))


def _expand(base, rrule, win_lo, win_hi):
    """Yield occurrence starts within [win_lo, win_hi].

    ponytail: FREQ=DAILY/WEEKLY/MONTHLY/YEARLY + INTERVAL + COUNT/UNTIL only.
    No BYDAY (uses DTSTART's weekday/day). Add `icalendar` dep if needed.
    """
    freq = rrule.get("FREQ", "")
    interval = int(rrule.get("INTERVAL", "1") or "1")
    count = int(rrule["COUNT"]) if "COUNT" in rrule else None
    until = None
    if "UNTIL" in rrule:
        u = rrule["UNTIL"]
        until = date(int(u[0:4]), int(u[4:6]), int(u[6:8]))
    is_dt = isinstance(base, datetime)
    cur = base
    n = 0
    while n < 2000:
        d = cur.date() if is_dt else cur
        if until and d > until:
            break
        if count is not None and n >= count:
            break
        if d > win_hi:
            break
        if d >= win_lo:
            yield cur
        n += 1
        if freq == "DAILY":
            cur = cur + timedelta(days=interval)
        elif freq == "WEEKLY":
            cur = cur + timedelta(weeks=interval)
        elif freq == "MONTHLY":
            cur = _add_months(cur, interval)
        elif freq == "YEARLY":
            cur = _add_months(cur, 12 * interval)
        else:
            break


def _span_days(start, end, all_day):
    """Extra days a multi-day event covers after its start day (0 = one day)."""
    if end is None or type(end) is not type(start):
        return 0
    sd = start.date() if isinstance(start, datetime) else start
    if all_day:
        ed = end - timedelta(days=1)           # DATE DTEND is exclusive
    else:
        ed = end.date()
        if end.time() == datetime.min.time() and ed > sd:
            ed -= timedelta(days=1)            # ends at midnight -> previous day
    return max(0, (ed - sd).days)


def parse_events(raw, win_lo, win_hi):
    events = []
    cur = None
    for line in _unfold(raw or ""):
        if line == "BEGIN:VEVENT":
            cur = {}
        elif line == "END:VEVENT":
            if cur and "start" in cur:
                base = cur["start"]
                span = timedelta(days=_span_days(base, cur.get("end"), cur["all_day"]))
                meta = {"all_day": cur["all_day"],
                        "summary": cur.get("summary", "(başlıksız)"),
                        "location": cur.get("location", ""),
                        "description": cur.get("description", "")}
                starts = (_expand(base, cur["rrule"], win_lo - span, win_hi)
                          if "rrule" in cur else [base])
                for occ in starts:
                    d = occ.date() if isinstance(occ, datetime) else occ
                    if d <= win_hi and d + span >= win_lo:  # overlaps the window
                        events.append({"start": occ, "last": d + span, **meta})
            cur = None
        elif cur is not None:
            if ":" not in line:
                continue
            name, value = line.split(":", 1)
            parts = name.split(";")
            key = parts[0].upper()
            params = {}
            for p in parts[1:]:
                if "=" in p:
                    pk, pv = p.split("=", 1)
                    params[pk.upper()] = pv
            if key == "DTSTART":
                cur["start"], cur["all_day"] = _parse_dt(value, params)
            elif key == "DTEND":
                cur["end"], _ = _parse_dt(value, params)
            elif key == "SUMMARY":
                cur["summary"] = _unescape(value).strip()
            elif key == "LOCATION":
                cur["location"] = _unescape(value).strip()
            elif key == "DESCRIPTION":
                cur["description"] = _unescape(value).strip()
            elif key == "RRULE":
                cur["rrule"] = _parse_rrule(value)
    events.sort(key=lambda e: (e["start"] if isinstance(e["start"], datetime)
                               else datetime(e["start"].year, e["start"].month,
                                             e["start"].day, tzinfo=TZ)))
    return events


def _edate(e):
    return e["start"].date() if isinstance(e["start"], datetime) else e["start"]


def _multi(e):
    return e["last"] != _edate(e)


def _ev_time(e):
    if _multi(e):
        a, b = _edate(e), e["last"]
        rng = f"{a.day} {TR_MONTHS[a.month][:3]}–{b.day} {TR_MONTHS[b.month][:3]}"
        return rng if e["all_day"] else f'{e["start"].strftime("%H:%M")} {rng}'
    return "tüm gün" if e["all_day"] else e["start"].strftime("%H:%M")


def _cell_label(e, day):
    """Month-cell text: start time on the first day of a timed event."""
    if not e["all_day"] and day == _edate(e):
        return f'{e["start"].strftime("%H:%M")} {_title(e)}'
    return e["summary"]  # red days are marked by the shaded cell, not a suffix


def _title(e):
    return e["summary"] + (" (kırmızı gün)" if e.get("tag") == "red" else "")


# ---------------------------------------------------------------- HTML
CSS = """
*{box-sizing:border-box;margin:0;padding:0}
body{background:#fff;color:#000;font-family:sans-serif;font-size:21px;
     line-height:1.35;padding:14px}
a{color:#000;text-decoration:none}
h1{font-size:30px;border-bottom:3px solid #000;padding-bottom:6px;margin-bottom:12px}
h2{font-size:23px;margin:18px 0 6px;border-bottom:2px solid #000}
.nav{margin:10px 0 14px}
.nav a{border:2px solid #000;padding:8px 16px;margin:0 10px 8px 0;display:inline-block;font-size:22px}
.glance{font-size:28px;font-weight:bold;margin:4px 0 14px;text-align:center}
.glance svg{margin:0 3px}
.city{white-space:nowrap}
.fc{table-layout:fixed;width:100%;margin:0 0 6px}
.fc td{border:none;text-align:center;padding:4px 1px;vertical-align:top}
.fd{font-size:16px}
.ft{font-size:17px;font-weight:bold;margin-top:2px}
.ft span{font-weight:normal}
.alert{border:3px solid #000;background:#000;color:#fff;padding:9px 12px;
       font-size:21px;font-weight:bold;margin:8px 0}
table{width:100%;border-collapse:collapse;margin-top:6px}
th,td{border:2px solid #000;padding:7px;text-align:left;font-size:19px}
th{background:#000;color:#fff}
.rain{font-weight:bold}
.day{margin-top:14px;border-top:2px solid #000;padding-top:6px}
.day .d{font-size:21px;font-weight:bold}
.ev{padding:6px 0 6px 10px;border-left:6px solid #000;margin:6px 0}
.ev .t{font-weight:bold;margin-right:10px}
.desc{font-size:18px;margin-top:4px}
.cal{table-layout:fixed}
.cal td{height:74px;padding:3px;vertical-align:top}
.cal .cb{height:68px;overflow:hidden}
.cal td.red{background:#bbb}
.modal{display:none;position:fixed;top:0;left:0;right:0;bottom:0;background:#fff}
.modal:target{display:block}
.mbox{border:3px solid #000;margin:16px;padding:14px;height:740px;overflow:auto}
.mbox h2{margin-top:6px}
.mx{float:right;padding:4px}
.cal .dnum{font-weight:bold;font-size:20px}
.cal .today{background:#000;color:#fff;border-radius:50%;padding:1px 9px}
.cal .ce{font-size:14px;line-height:1.25;margin-top:2px;border-left:3px solid #000;
         padding-left:3px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.hint{font-size:18px;margin-top:10px}
.foot{margin-top:20px;font-size:16px;border-top:2px solid #000;padding-top:6px}
"""

NAV = ('<div class=nav><a href="/">Pano</a>'
       '<a href="/calendar">Takvim</a></div>')


def page(title, body, refresh=False):
    meta = (f'<meta http-equiv="refresh" content="{REFRESH_SECONDS}">'
            if refresh and REFRESH_SECONDS > 0 else "")
    return (f"<!doctype html><html lang=tr><head><meta charset=utf-8>"
            f'<meta name=viewport content="width=600">{meta}'
            f"<title>{html.escape(title)}</title><style>{CSS}</style></head>"
            f"<body>{body}"
            f'<div class=foot>{datetime.now(TZ).strftime("%d.%m.%Y %H:%M")} · '
            f'<a href="/">Pano</a> · <a href="/calendar">Takvim</a></div>'
            f"</body></html>")


def _daylink(d):
    return f"/calendar?view=day&d={d.isoformat()}"


def render_dashboard():
    try:
        ist = get_weather(LAT, LON)
        gbg = get_weather(LAT2, LON2, hourly=True, daily=True, tz=TZNAME2)
    except Exception as e:
        return page("Hava", NAV + f"<h1>Hava durumu alınamadı</h1>"
                    f"<p>{html.escape(str(e))}</p>", refresh=True)
    ci, cg = ist["current"], gbg["current"]
    body = [NAV]
    # 1) glance: each city — flat icon + current degree
    body.append(
        '<div class=glance>'
        f'<span class=city>{html.escape(PLACE)} {wicon(int(ci["weather_code"]), 34)}'
        f'{round(ci["temperature_2m"])}°</span>  ·  '
        f'<span class=city>{html.escape(PLACE2)} {wicon(int(cg["weather_code"]), 34)}'
        f'{round(cg["temperature_2m"])}°</span></div>')
    # 2) one narrow row: Göteborg every 3h — time / icon / temp
    h = gbg["hourly"]
    cells = []
    for i in range(0, len(h["time"]), HOURLY_STEP):
        cells.append(f'<td><div class=fd>{h["time"][i][11:16]}</div>'
                     f'{wicon(int(h["weather_code"][i]), 28)}'
                     f'<div class=ft>{round(h["temperature_2m"][i])}°</div></td>')
    body.append(f'<table class=fc><tr>{"".join(cells)}</tr></table>')
    # 3) warnings for Göteborg (only when a threshold trips)
    for a in weather_alerts(gbg):
        body.append(f'<div class=alert>UYARI — {html.escape(a)}</div>')
    # 5) calendar preview
    body.append("<h2>Takvim</h2>")
    today = datetime.now(TZ).date()
    evs = get_events(today, today + timedelta(days=PREVIEW_DAYS - 1))
    if evs is None:
        body.append('<p class=hint>ICS linki ayarlanmadı (KOBO_ICS_URL).</p>')
    elif not evs:
        body.append(f"<p>Önümüzdeki {PREVIEW_DAYS} günde etkinlik yok.</p>")
    else:
        for e in evs[:PREVIEW_N]:
            dd = max(_edate(e), today)  # ongoing multi-day events link to today
            when = (_ev_time(e) if _multi(e) else
                    f'{TR_DAYS[dd.weekday()]} {dd.day} {TR_MONTHS[dd.month][:3]} {_ev_time(e)}')
            body.append(f'<a href="{_daylink(dd)}"><div class=ev>'
                        f'<span class=t>{when}</span>{html.escape(_title(e))}</div></a>')
    return page(f"{PLACE2} Pano", "".join(body), refresh=True)


def _no_ics_msg():
    return (NAV + "<h1>Takvim</h1><p>ICS linki ayarlanmadı.</p>"
            "<p class=hint>Sunucuyu <b>KOBO_ICS_URL</b> ortam değişkeni ile başlat. "
            "iCloud: Takvim → paylaş → Herkese Açık Takvim → linki kopyala. "
            "Google: Ayarlar → takvim → “Herkese açık adres (iCal)”.</p>")


def render_list():
    today = datetime.now(TZ).date()
    events = get_events(today, today + timedelta(days=LIST_DAYS))
    if events is None:
        return page("Takvim", _no_ics_msg())
    events = events[:LIST_MAX]
    body = [NAV, "<h1>Yaklaşan</h1>",
            '<div class=nav><a href="/calendar">Ay görünümü</a></div>']
    if not events:
        body.append("<p>Yaklaşan etkinlik yok.</p>")
    cur_day = None
    for e in events:
        d = max(_edate(e), today)  # ongoing multi-day events sit under today
        if d != cur_day:
            cur_day = d
            label = ("Bugün" if d == today else
                     "Yarın" if d == today + timedelta(days=1) else
                     f"{d.day} {TR_MONTHS[d.month]} {TR_DAYS[d.weekday()]}")
            body.append(f'<div class=day><div class=d>{label}</div>')
        body.append(f'<a href="{_daylink(d)}"><div class=ev>'
                    f'<span class=t>{_ev_time(e)}</span>'
                    f'{html.escape(_title(e))}</div></a>')
    return page("Takvim", "".join(body))


def render_day(dstr):
    try:
        y, m, dd = (int(x) for x in dstr.split("-"))
        day = date(y, m, dd)
    except Exception:
        day = datetime.now(TZ).date()
    events = get_events(day, day)
    if events is None:
        return page("Takvim", _no_ics_msg())
    label = f"{day.day} {TR_MONTHS[day.month]} {day.year}, {TR_DAYS[day.weekday()]}"
    body = [NAV, f"<h1>{label}</h1>",
            f'<div class=nav><a href="/calendar?m={day.year}-{day.month:02d}">Ay</a>'
            f'<a href="/calendar?view=list">Liste</a></div>']
    if not events:
        body.append("<p>Bu gün etkinlik yok.</p>")
    body.extend(_ev_detail(e) for e in events)
    return page("Gün", "".join(body))


def _ev_detail(e):
    """Full event block: time/range, title, location, description."""
    out = [f'<div class=ev><div class=t>{_ev_time(e)} — {html.escape(_title(e))}</div>']
    if e.get("location"):
        out.append(f'<div class=desc>Yer: {html.escape(e["location"])}</div>')
    if e.get("description") and e.get("tag") not in ("red", "day"):  # holiday feed text is Google boilerplate
        out.append(f'<div class=desc>{html.escape(e["description"]).replace(chr(10), "<br>")}</div>')
    out.append('</div>')
    return "".join(out)


def render_month(mstr):
    """Default calendar view: one month grid, changed only via prev/next links.

    Tapping a day with events opens a CSS :target modal (fragment link, no
    page load, no JS).
    """
    today = datetime.now(TZ).date()
    try:
        y, m = (int(x) for x in mstr.split("-"))
        first = date(y, m, 1)
    except Exception:
        first = today.replace(day=1)
    end = _add_months(first, MONTHS_SHOWN)
    events = get_events(first, end - timedelta(days=1))
    if events is None:
        return page("Takvim", _no_ics_msg())
    by_day = {}
    for e in events:  # a multi-day event lands in every day it covers
        d = max(_edate(e), first)
        while d <= min(e["last"], end - timedelta(days=1)):
            by_day.setdefault(d, []).append(e)
            d += timedelta(days=1)
    prevm, nextm = _add_months(first, -1), _add_months(first, 1)
    nav = (f'<div class=nav>'
           f'<a href="/calendar?m={prevm.year}-{prevm.month:02d}">&lt; Önceki</a>'
           f'<a href="/calendar#today">Bugün</a>'
           f'<a href="/calendar?m={nextm.year}-{nextm.month:02d}">Sonraki &gt;</a>'
           f'<a href="/calendar?view=list">Liste</a></div>')
    grids = "".join(_month_grid(_add_months(first, k), today, by_day)
                    for k in range(MONTHS_SHOWN))
    return page("Takvim", NAV + nav + grids)


def _month_grid(first, today, by_day):
    nextm = _add_months(first, 1)
    head = f'<h1 id="m{first.year}-{first.month:02d}">{TR_MONTHS[first.month]} {first.year}</h1>'
    thead = "<tr>" + "".join(f"<th>{d}</th>" for d in TR_DAYS) + "</tr>"

    cells, modals = [], []
    cur = first - timedelta(days=first.weekday())  # back to Monday
    while cur < nextm or cur.weekday() != 0:
        in_month = cur.month == first.month
        is_today = cur == today and in_month
        anchor = ' id=today' if is_today else ""
        cls = " class=today" if is_today else ""
        day_evs = by_day.get(cur, []) if in_month else []
        if any(e.get("tag") == "red" for e in day_evs):
            anchor += ' class=red'
        dnum = f'<span{cls}>{cur.day}</span>' if in_month else ""
        evs = ""
        if day_evs:
            mid = f"d{cur.isoformat()}"
            dnum = f'<a href="#{mid}">{dnum}</a>'
            evs = "".join(f'<a href="#{mid}"><div class=ce>{html.escape(_cell_label(e, cur))}</div></a>'
                          for e in day_evs[:CELL_EVENTS])
            if len(day_evs) > CELL_EVENTS:
                evs += f'<a href="#{mid}"><div class=ce>+{len(day_evs) - CELL_EVENTS}</div></a>'
            label = f"{cur.day} {TR_MONTHS[cur.month]} {TR_DAYS[cur.weekday()]}"
            modals.append(f'<div class=modal id={mid}><div class=mbox>'
                          f'<a class=mx href="#">{CLOSE_X}</a><h2>{label}</h2>'
                          f'{"".join(_ev_detail(e) for e in day_evs)}</div></div>')
        cells.append(f'<td{anchor}><div class=cb><span class="dnum">{dnum}</span>{evs}</div></td>')
        cur += timedelta(days=1)
    rows = ["<tr>" + "".join(cells[i:i+7]) + "</tr>" for i in range(0, len(cells), 7)]
    return f'{head}<table class=cal>{thead}{"".join(rows)}</table>{"".join(modals)}'


# ---------------------------------------------------------------- kobo
REPORT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "reports")

# Read-only device survey. Run on the Kobo as root: wget -qO- HOST/kobo/probe | sh
PROBE_SH = r"""#!/bin/sh
OUT=/tmp/kobo-probe.txt
{
echo "== id"; id
echo "== uname"; uname -a
echo "== init / chroot check"; cat /proc/1/cmdline | tr '\0' ' '; echo; ls / | tr '\n' ' '; echo
echo "== mounts"; mount
echo "== boot flags"; for f in /boot/flags/*; do echo "$f=$(cat "$f" 2>/dev/null)"; done
echo "== xorg (KoBox)"; ls /xorg 2>&1 | tr '\n' ' '; echo; cat /xorg/etc/alpine-release 2>&1; ls /xorg/scripts 2>&1
echo "== fbink"; ls -la /opt/bin/fbink 2>&1
echo "== ibxd"; ls -la /opt/ibxd /tmp/X_program 2>&1
echo "== input devices"; cat /proc/bus/input/devices 2>&1
echo "== net"; ip addr 2>&1 | grep -E "inet |^[0-9]"
echo "== memory"; free 2>&1
echo "== disk"; df -h 2>&1
echo "== battery"; for d in /sys/class/power_supply/*; do echo "$d capacity=$(cat $d/capacity 2>/dev/null) status=$(cat $d/status 2>/dev/null)"; done
echo "== processes"; ps 2>&1
echo "== busybox"; busybox 2>&1 | head -1; wget --help 2>&1 | grep -i post
} > $OUT 2>&1
wget -q -O- --post-file=$OUT "http://@HOST@/kobo/report" && echo "OK: rapor gonderildi" || { echo "Gonderilemedi, cikti:"; cat $OUT; }
"""


# ---------------------------------------------------------------- server
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def log_request(self, code="-", size="-"):
        # one line per request: who, what, and the browser (handy to spot the Kobo)
        print(f'{time.strftime("%H:%M:%S")} {self.client_address[0]} {code} {self.path} '
              f'| {self.headers.get("User-Agent", "-")}', flush=True)

    def _send(self, body, code=200, ctype="text/html; charset=utf-8"):
        if ASCII and isinstance(body, str) and "html" in ctype:
            body = asciify(body)
        data = body.encode("utf-8") if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(data)

    def do_HEAD(self):
        self.do_GET()

    def do_POST(self):
        # Kobo-side scripts send their output here so it can be read on the server.
        if urllib.parse.urlparse(self.path).path != "/kobo/report":
            return self._send("404", 404, "text/plain; charset=utf-8")
        n = min(int(self.headers.get("Content-Length") or 0), 1_000_000)
        body = self.rfile.read(n)
        os.makedirs(REPORT_DIR, exist_ok=True)
        path = os.path.join(REPORT_DIR, time.strftime("%Y%m%d-%H%M%S") + ".txt")
        with open(path, "wb") as f:
            f.write(body)
        print(f"report saved: {path} ({len(body)} bytes)", flush=True)
        self._send("saved\n", 200, "text/plain; charset=utf-8")

    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        q = urllib.parse.parse_qs(u.query)
        try:
            if u.path == "/favicon.ico":
                self._send(b"", 204, "image/x-icon")
            elif u.path == "/kobo/probe":
                host = self.headers.get("Host", f"127.0.0.1:{PORT}")
                self._send(PROBE_SH.replace("@HOST@", host), 200, "text/plain; charset=utf-8")
            elif u.path == "/":
                self._send(render_dashboard())
            elif u.path == "/calendar":
                view = q.get("view", ["month"])[0]
                if view == "list":
                    self._send(render_list())
                elif view == "day":
                    self._send(render_day(q.get("d", [""])[0]))
                else:
                    self._send(render_month(q.get("m", [""])[0]))
            else:
                self._send(NAV + "<h1>404</h1>", 404)
        except Exception as e:
            self._send(page("Hata", NAV + f"<h1>Sunucu hatası</h1>"
                            f"<p>{html.escape(str(e))}</p>"), 500)


# ---------------------------------------------------------------- selftest
def selftest():
    sample = (
        "BEGIN:VCALENDAR\r\n"
        "BEGIN:VEVENT\r\nDTSTART:20261101T090000Z\r\nSUMMARY:Tek sefer\r\n"
        "LOCATION:Ofis\r\nDESCRIPTION:satir1\\nsatir2\r\nEND:VEVENT\r\n"
        "BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261103\r\nSUMMARY:Tum gun; kacis\\, test\r\nEND:VEVENT\r\n"
        "BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Istanbul:20261105T140000\r\n"
        "SUMMARY:Haftalik\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\nEND:VEVENT\r\n"
        "BEGIN:VEVENT\r\nDTSTART:20261201T080000Z\r\nSUMMARY:Cok uzak\r\nEND:VEVENT\r\n"
        "BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261110\r\nDTEND;VALUE=DATE:20261113\r\n"
        "SUMMARY:Uc gun\r\nEND:VEVENT\r\n"
        "BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261028\r\nDTEND;VALUE=DATE:20261103\r\n"
        "SUMMARY:Tasan\r\nEND:VEVENT\r\n"
        "END:VCALENDAR\r\n"
    )
    lo, hi = date(2026, 11, 1), date(2026, 11, 30)
    evs = parse_events(sample, lo, hi)
    titles = [e["summary"] for e in evs]
    assert "Tek sefer" in titles, titles
    assert titles.count("Haftalik") == 4, titles
    assert "Cok uzak" not in titles, "Dec event must be outside Nov window"
    one = [e for e in evs if e["summary"] == "Tek sefer"][0]
    assert one["location"] == "Ofis", one
    assert one["description"] == "satir1\nsatir2", repr(one["description"])
    allday = [e for e in evs if e["summary"].startswith("Tum gun")][0]
    assert allday["all_day"] is True
    assert allday["summary"] == "Tum gun; kacis, test", repr(allday["summary"])
    three = [e for e in evs if e["summary"] == "Uc gun"][0]
    assert three["last"] == date(2026, 11, 12), three["last"]  # DTEND exclusive
    spill = [e for e in evs if e["summary"] == "Tasan"]
    assert spill and spill[0]["last"] == date(2026, 11, 2), spill  # started in Oct, overlaps Nov
    assert _cell_label(one, date(2026, 11, 1)).endswith("Tek sefer") and ":" in _cell_label(one, date(2026, 11, 1))
    assert _add_months(date(2026, 1, 31), 1) == date(2026, 2, 28)
    assert asciify("İstanbul Yağış Göteborg") == "Istanbul Yagis Goteborg"
    one["tag"] = "red"
    assert _title(one) == "Tek sefer (kırmızı gün)", _title(one)
    fake = {"daily": {"time": ["2026-11-01"], "weather_code": [75],
            "precipitation_sum": [25], "wind_speed_10m_max": [60],
            "temperature_2m_min": [-8], "temperature_2m_max": [2],
            "precipitation_probability_max": [90]}}
    al = weather_alerts(fake)
    assert any("kar" in a for a in al) and any("rüzgar" in a for a in al), al
    print("selftest OK:", len(evs), "events;", len(al), "alerts")


if __name__ == "__main__":
    if "--selftest" in sys.argv:
        selftest()
        sys.exit(0)
    srv = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"Kobo dashboard: http://0.0.0.0:{PORT}/  "
          f"(glance={PLACE}, detail={PLACE2}, feeds={len(_sources())}, "
          f"ascii={'on' if ASCII else 'off'})")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
