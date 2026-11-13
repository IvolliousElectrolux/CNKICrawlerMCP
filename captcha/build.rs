fn main() {
    println!("cargo:rerun-if-changed=model/common.onnx");
    println!("cargo:rerun-if-changed=model/common_det.onnx");
    for name in ["model/common.onnx", "model/common_det.onnx"] {
        if !std::path::Path::new(name).is_file() {
            panic!("missing {name}. GitHub Actions downloads the ddddocr models before cargo build");
        }
    }
}
