import { createContext } from "react-router";
import type { ApiAuth } from "./api";

/** Where server renders fetch from, and the token that gives them their own rate limit. */
export const serverEnv = createContext<ApiAuth>();
