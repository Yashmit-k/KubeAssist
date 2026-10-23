import { FileDown } from "lucide-react";
import { Button, CopyButton } from "./ui";

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
  const a = Object.assign(document.createElement("a"), { href: url, download: filename });
  a.click();
  URL.revokeObjectURL(url);
}

/** Copy or download an incident report (Markdown). */
export default function ReportButtons({ markdown, filename }: { markdown: string; filename: string }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <CopyButton text={markdown} label="Copy report" className="border border-line px-2.5 py-1.5" />
      <Button variant="ghost" className="text-xs" onClick={() => download(filename, markdown)} title="Download the incident report as Markdown">
        <FileDown className="size-3.5" /> Download .md
      </Button>
    </div>
  );
}
