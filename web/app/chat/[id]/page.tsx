import { notFound } from "next/navigation";
import { ChatShell } from "../../../components/chat-shell";
export default async function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  if (
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id)
  )
    notFound();
  return <ChatShell conversationId={id} />;
}
