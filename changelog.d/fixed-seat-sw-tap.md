- Tapping a notice from the seat opens the Cards list also before the service
  worker controls any page: the worker looks at uncontrolled windows too,
  navigates one before it focuses it (a worker cannot navigate a page it does
  not control, and focusing first could use up the tap's permission to open a
  window) and otherwise opens the list in a window of its own. It still takes
  no control of pages and handles no requests.
