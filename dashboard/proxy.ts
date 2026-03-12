import { NextRequest, NextResponse } from "next/server";

// Proxy middleware: used in development only. In the embedded Go binary,
// the server handles /api/proxy/ directly with the admin macaroon attached.
// Next.js middleware is not included in `output: 'export'` static builds.

const APERTURE_URL = process.env.APERTURE_URL ?? "http://localhost:8081";
const APERTURE_MACAROON = process.env.APERTURE_MACAROON ?? "";

const SAFE_SEGMENT = /^[\w-]+$/;

export async function proxy(req: NextRequest) {
  const segments = req.nextUrl.pathname
    .replace(/^\/api\/proxy\//, "")
    .split("/")
    .filter(Boolean);

  if (segments.some((seg) => !SAFE_SEGMENT.test(seg))) {
    return new NextResponse("bad request", { status: 400 });
  }

  const path = segments.join("/");
  const url = new URL(`/api/admin/${path}`, APERTURE_URL);
  url.search = req.nextUrl.search;

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (APERTURE_MACAROON) {
    headers["Grpc-Metadata-Macaroon"] = APERTURE_MACAROON;
  }

  let body: string | undefined;
  if (req.method !== "GET" && req.method !== "HEAD") {
    body = await req.text();
  }

  const upstream = await fetch(url.toString(), {
    method: req.method,
    headers,
    body,
  });

  const data = await upstream.text();
  return new NextResponse(data, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}

export const config = {
  matcher: "/api/proxy/:path*",
};
