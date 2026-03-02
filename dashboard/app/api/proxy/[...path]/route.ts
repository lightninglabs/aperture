import { NextRequest, NextResponse } from "next/server";

const APERTURE_URL = process.env.APERTURE_URL ?? "http://localhost:8081";
const APERTURE_MACAROON = process.env.APERTURE_MACAROON ?? "";

async function proxy(req: NextRequest, params: { path: string[] }) {
  const path = params.path.join("/");
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

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ path: string[] }> },
) {
  return proxy(req, await params);
}

export async function PUT(
  req: NextRequest,
  { params }: { params: Promise<{ path: string[] }> },
) {
  return proxy(req, await params);
}

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ path: string[] }> },
) {
  return proxy(req, await params);
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ path: string[] }> },
) {
  return proxy(req, await params);
}
