"""3D sidecar shared by LvW-harness and usb-llms (MIT).

Turns one picture into a GLB mesh with TripoSR (MIT, VAST AI Research and Stability AI).
TripoSR's source is downloaded at install time and imported from --tsr; its compiled
`torchmcubes` dependency is replaced by PyMCubes, so nothing needs a compiler.

POST /3d  {"image": "<base64 or data URL>", "remove_bg": true, "resolution": 256, "ratio": 0.85}
          -> model/gltf-binary
GET  /health
"""

import argparse
import base64
import io
import json
import os
import sys
import threading
import types
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def shim_torchmcubes():
    import mcubes
    import numpy as np
    import torch

    def marching_cubes(volume, iso):
        verts, faces = mcubes.marching_cubes(volume.detach().cpu().numpy(), iso)
        # torchmcubes gives (x, y, z) with x along the last axis and TripoSR swaps them back,
        # while PyMCubes gives index order, so reverse here.
        verts = verts[:, [2, 1, 0]]
        return torch.from_numpy(verts.astype(np.float32)), torch.from_numpy(faces.astype(np.int64))

    sys.modules["torchmcubes"] = types.SimpleNamespace(marching_cubes=marching_cubes)


class Engine:
    def __init__(self, tsr_dir, device):
        shim_torchmcubes()
        sys.path.insert(0, tsr_dir)
        import torch
        from tsr.system import TSR

        if device == "auto":
            device = "cuda" if torch.cuda.is_available() else "mps" if torch.backends.mps.is_available() else "cpu"
        self.torch = torch
        self.device = device
        self.model = TSR.from_pretrained("stabilityai/TripoSR", config_name="config.yaml", weight_name="model.ckpt")
        self.model.renderer.set_chunk_size(8192)
        self.model.to(device)
        self.rembg = None
        self.lock = threading.Lock()

    def make(self, image_bytes, remove_bg=True, resolution=256, ratio=0.85):
        import numpy as np
        from PIL import Image
        from tsr.utils import remove_background, resize_foreground, to_gradio_3d_orientation

        image = Image.open(io.BytesIO(image_bytes))
        if remove_bg:
            if self.rembg is None:
                import rembg

                self.rembg = rembg.new_session()
            image = remove_background(image, self.rembg)
            image = resize_foreground(image, ratio)
            arr = np.array(image).astype(np.float32) / 255.0
            arr = arr[:, :, :3] * arr[:, :, 3:4] + (1 - arr[:, :, 3:4]) * 0.5
            image = Image.fromarray((arr * 255.0).astype(np.uint8))
        else:
            image = image.convert("RGB")
        with self.lock, self.torch.no_grad():
            try:
                codes = self.model([image], device=self.device)
            except (RuntimeError, NotImplementedError):
                if self.device != "mps":
                    raise
                # Some ops are missing on Apple GPUs; fall back to the CPU for good.
                self.device = "cpu"
                self.model.to("cpu")
                codes = self.model([image], device="cpu")
            try:
                mesh = self.model.extract_mesh(codes, True, resolution=resolution)[0]
            except RuntimeError as e:
                if "0 elements" not in str(e):
                    raise
                mesh = None
        if mesh is None or len(mesh.vertices) == 0:
            raise ValueError("No single object found in the picture. Use one object on a plain background, fully in view.")
        if mesh.volume < 0:
            mesh.invert()
        mesh = to_gradio_3d_orientation(mesh)
        return mesh.export(file_type="glb")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=7881)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--tsr", required=True, help="folder holding TripoSR's tsr package")
    ap.add_argument("--device", default="auto")
    args = ap.parse_args()
    engine = Engine(args.tsr, args.device)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *a):
            sys.stderr.write("3d: " + fmt % a + "\n")

        def reply(self, code, body, ctype="application/json"):
            self.send_response(code)
            self.send_header("content-type", ctype)
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path == "/health":
                return self.reply(200, json.dumps({"ok": True, "device": engine.device}).encode())
            self.reply(404, b'{"error":"not found"}')

        def do_POST(self):
            if self.path != "/3d":
                return self.reply(404, b'{"error":"not found"}')
            try:
                req = json.loads(self.rfile.read(int(self.headers.get("content-length", 0))))
                data = req["image"]
                if data.startswith("data:"):
                    data = data.split(",", 1)[1]
                glb = engine.make(
                    base64.b64decode(data),
                    remove_bg=req.get("remove_bg", True),
                    resolution=int(req.get("resolution", 256)),
                    ratio=float(req.get("ratio", 0.85)),
                )
                self.reply(200, glb, "model/gltf-binary")
            except Exception as e:  # report every failure to the caller
                self.reply(500, json.dumps({"error": f"{type(e).__name__}: {e}"}).encode())

    print(f"3d sidecar ready on {args.host}:{args.port} ({engine.device})", flush=True)
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    os.environ.setdefault("PYTORCH_ENABLE_MPS_FALLBACK", "1")
    main()
