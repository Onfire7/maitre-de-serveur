// How long a test that renders the whole Console (the App on the demo
// Fleet, walking its tabs) may take. One takes a few seconds alone, but far
// longer on a loaded machine or a shared CI runner with every test file's
// worker starting at once; give each of these files' describe this, not a
// number of its own.
export const WHOLE_APP_TIMEOUT = 120_000;
