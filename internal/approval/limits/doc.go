// Package limits holds the rules of the daily card limit, and the count from
// the first card.
//
// A card is a request that the owner approves or denies by hand. The daily
// card limit is how many approved cards a project may have in one day. It
// bounds what a forged approval could do: once a project's day is full, a
// further approve is recorded and not applied. Such an approve is "held", and
// its card comes back to the owner as pending, to be decided again.
//
// The rules, each per project:
//
//   - The limit starts at Default (12). No limit is under Min (1) or above
//     Max (40).
//   - Lowering is one key: a number no higher than the present one is taken
//     as it is.
//   - Raising is typed, and by half at most: the new number may be at most
//     the present one times three, divided by two and rounded down, and at
//     most Max. So 12 rises to 18, 18 to 27 and 27 to 40. A limit of 1
//     cannot be raised, and 2 rises only to 3.
//   - One raise a day.
//   - The suggestion is twice the busiest of the 14 calendar days before
//     today, never under 6 and never above Max. Days that reached the limit
//     and days with an alert are left out, so that a run of forged cards
//     cannot push the suggestion up.
//   - A card use is an approve that was not held. An approve is held when the
//     project's uses that day are already at or above the limit. A deny is
//     never held and is never a use.
//
// A day is a calendar day of the clock the caller hands in, read in that
// clock's own location. Every function that needs the time takes it as a
// value, "now"; the package never reads a clock by itself. A decision made at
// 23:59 and one made at 00:00 in that location are on two days, whatever
// their UTC dates are.
//
// The count from the first card is a different thing with a similar purpose:
// Count says whether one change, given as plain numbers, would have met the
// terms of the First template (FirstTerms). It is recorded for every request
// so that the owner can see how many cards a standing approval would have
// taken.
//
// The package is pure. It opens no file, keeps no store and imports nothing
// of this repository. When a rule says no, the answer is a *Refusal with a
// kind and a one-line reason written by this package; the caller maps it to
// its own refusal code.
package limits
