// While a toast is up and the sidebar is too narrow to hold it, the page
// slides right to make room (decision 294, layout.ts): nothing of the
// Console may float over a Service's native view, a toast included.
import { useEffect } from "react";
import { useSonner } from "sonner";
import { setToastRoom, toastRoom } from "./layout";

export function ToastRoom({ sidebar }: { sidebar: number }) {
  const { toasts } = useSonner();
  const room = toasts.length ? toastRoom(sidebar) : 0;
  useEffect(() => {
    setToastRoom(room);
    return () => setToastRoom(0);
  }, [room]);
  return null;
}
