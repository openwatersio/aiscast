import { redirect } from "react-router";
import { SAILORS_PATH } from "../lib/tracking";

export const loader = () => redirect(SAILORS_PATH, 301);
