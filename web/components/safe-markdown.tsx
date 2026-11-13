import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
export function safeLink(value: string | undefined): string | undefined {
  if (!value) return undefined;
  try {
    const u = new URL(value);
    if (
      (u.protocol === "https:" || u.protocol === "http:") &&
      !u.username &&
      !u.password
    )
      return u.href;
  } catch {}
  return undefined;
}
export function SafeMarkdown({ text }: { text: string }) {
  return (
    <div className="markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        components={{
          img: ({ alt }) => (
            <span className="inert-media">
              [Image not loaded{alt ? `: ${alt}` : ""}]
            </span>
          ),
          a: ({ href, children }) => {
            const safe = safeLink(href);
            return safe ? (
              <a
                href={safe}
                target="_blank"
                rel="noopener noreferrer"
                referrerPolicy="no-referrer"
              >
                {children}
              </a>
            ) : (
              <span>{children}</span>
            );
          },
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
