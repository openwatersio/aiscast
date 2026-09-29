import { ChevronLeft } from "lucide-react";
import type { ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { IconLink } from "./IconButton";

/** The bar at the top of a stack entry: the way back, and whatever acts on the entry. */
export function PanelHeader({ back, actions }: { back?: string; actions?: ReactNode }) {
  if (!back && !actions) return null;
  return (
    <div className="flex items-center gap-1 px-3 pt-3">
      {back && <BackButton parent={back} />}
      <div className="flex-1" />
      {actions}
    </div>
  );
}

/**
 * Within the app it steps back through history, so it returns to whatever pushed this entry,
 * as the browser's own back does. After a direct visit there is nothing to return to, and it
 * goes to `parent` instead.
 */
function BackButton({ parent }: { parent: string }) {
  const navigate = useNavigate();
  const location = useLocation();
  // React Router gives the entry the app loaded on the key "default"; every entry pushed
  // since has its own, and keeps it across a reload.
  const pushed = location.key !== "default";
  return (
    <IconLink
      icon={ChevronLeft}
      label="Back"
      to={parent}
      onClick={(e) => {
        if (!pushed) return;
        e.preventDefault();
        navigate(-1);
      }}
    />
  );
}
