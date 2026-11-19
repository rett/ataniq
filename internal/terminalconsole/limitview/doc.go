// Package limitview is the console's screen for the daily card limit of one
// project.
//
// The parent (the cockpit) hands the view a limits.View, which it got from its
// backend; the view calls no backend itself. It shows the project's name, the
// card uses of the 14 days before today with each day's count, the days that
// the suggestion left out and why (the day reached the limit, or it had an
// alert), the present number and the suggestion.
//
// Keys. Enter with nothing typed accepts the suggestion when it is no higher
// than the present number; when it is higher, the view says that a higher
// number has to be typed. Digits type a number and Enter sets it. Whether a
// number is allowed is the rule of package limits (limits.Check), and a
// refusal shows limits' own one-line reason. Backspace edits the typed
// number. Lowering to a number from which the limit can hardly rise again
// asks once more, with limits.LowerWarning. Esc asks the parent to close the
// view. No single key raises the number: a raise is typed digits and then
// Enter.
//
// A change leaves the view as a limits.Change in the Action that Update
// returns. The view applies nothing itself: it goes on showing the old View
// until the parent hands the new one to SetView.
//
// The view holds no text of the lead. The project's name is configuration,
// still drawn cleaned (only printable characters) and bounded. Of this
// repository the package imports only limits.
package limitview
