import { proxy } from "../../../lib/proxy";
export const runtime = "nodejs";
export const dynamic = "force-dynamic";
async function handle(
  request: Request,
  { params }: { params: Promise<{ path: string[] }> },
) {
  return proxy(request, (await params).path, {
    origin: process.env.WEBCHAT_BROWSER_ORIGIN ?? "http://localhost:13000",
    upstream: process.env.WEBCHAT_API_URL ?? "http://127.0.0.1:18081",
    secret: process.env.WEBCHAT_PROXY_SECRET ?? "",
  });
}
export { handle as GET, handle as POST };
