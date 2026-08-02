"""Date arguments the assistant supplies for order lookups.

A 1.5B model will happily emit "12th July" or "last Tuesday" for a date field.
Passing that through would make the backend ignore the filter and quietly widen
the window back to every order the customer has — so the tool validates and
asks, rather than answering confidently about the wrong dates.
"""

from mcp_gateway.tenants import _bad_date, _clean_date


def test_accepts_iso_dates():
    assert _clean_date("2026-07-12") == "2026-07-12"
    assert _clean_date("  2026-07-12  ") == "2026-07-12"


def test_blank_is_not_an_error():
    # Absent simply means "no explicit range" — the rolling window applies.
    assert _clean_date("") == ""
    assert _clean_date(None) == ""


def test_rejects_everything_a_small_model_might_invent():
    for junk in ("12th July", "last Tuesday", "2026-13-01", "07/12/2026", "yesterday", "2026-07"):
        assert _clean_date(junk) == "", f"{junk!r} must not be accepted as a date"


def test_bad_date_tells_the_assistant_to_ask_not_guess():
    err = _bad_date("date_from", "last Tuesday")
    assert err["error"] == "bad_date"
    assert err["field"] == "date_from"
    assert "YYYY-MM-DD" in err["_action_for_assistant"]
    # The instruction must forbid narrating unfetched results — that is the
    # failure mode this guard exists to prevent.
    assert "not describe results" in err["_action_for_assistant"]
