import { defineMiddleware } from "astro:middleware";

// `?station=<id>` was how the old viewer opened a station, and it is in the README, the
// token page, and in links volunteers have already shared. It costs one redirect to keep
// those working, and the station id is a permalink people are told to hand out.
export const onRequest = defineMiddleware((context, next) => {
  const station = context.url.searchParams.get("station");
  if (station && context.url.pathname === "/") {
    return context.redirect(`/stations/${station}`, 301);
  }
  return next();
});
