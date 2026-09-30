import { redirect } from "react-router";

// In production the website answers /ais/ with its landing page and this route is never
// reached. It keeps the dev server and preview deploys from opening on a 404.
export function loader() {
  throw redirect("/vessels");
}

export function clientLoader() {
  throw redirect("/vessels");
}
