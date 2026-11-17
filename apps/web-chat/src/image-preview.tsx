import { useEffect, useRef, useState } from "react";
import { fileBlob } from "../../../packages/api-client/session";

export function ImagePreview({id, name, onDownload}: {id:string; name:string; onDownload:()=>void}) {
  const container = useRef<HTMLDivElement>(null);
  const [preview, setPreview] = useState("");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const element = container.current;
    if (!element) return;
    const abort = new AbortController();
    let url = "";
    const observer = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) return;
      observer.disconnect();
      fileBlob("/files/" + id + "/thumbnail", abort.signal).then((blob) => {
        if (abort.signal.aborted) return;
        url = URL.createObjectURL(blob);
        setPreview(url);
      }).catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      });
    }, {rootMargin:"120px"});
    observer.observe(element);
    return () => {
      observer.disconnect();
      abort.abort();
      if (url) URL.revokeObjectURL(url);
    };
  }, [id]);

  return <div className="image-attachment" ref={container}>
    {preview
      ? <button type="button" className="image-thumb" onClick={onDownload} title="Tải ảnh gốc">
          <img src={preview} alt={name} />
        </button>
      : <div className="image-placeholder">{failed ? "Không có ảnh xem trước" : "Đang tải ảnh…"}</div>}
    <div className="image-caption"><span title={name}>{name}</span><button onClick={onDownload}>Tải ảnh</button></div>
  </div>;
}
