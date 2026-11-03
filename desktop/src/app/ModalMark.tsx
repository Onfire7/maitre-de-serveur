// Tells the page layer a modal is open for as long as this is mounted, so
// the Service page hides behind its stand-in (decision 293). The dialog,
// sheet and alert dialog primitives mount one inside their content.
import { useEffect } from "react";
import { modalClosed, modalOpened } from "./layout";

export function ModalMark() {
  useEffect(() => {
    modalOpened();
    return modalClosed;
  }, []);
  return null;
}
