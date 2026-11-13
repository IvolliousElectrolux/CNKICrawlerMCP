use std::io::{BufRead, Write};

use anyhow::{bail, Context, Result};
use base64::Engine;
use ddddocr::{BBox, CharacterProbability, Ddddocr};
use image::DynamicImage;
use serde::Deserialize;

#[derive(Deserialize)]
struct Job {
    image: String,
    words: Vec<String>,
}

#[derive(Clone, Copy, serde::Serialize)]
struct Point {
    x: i32,
    y: i32,
}

struct Hit {
    text: String,
    prob: CharacterProbability,
    point: Point,
}

fn main() -> Result<()> {
    let stdout = std::io::stdout();
    let mut out = std::io::BufWriter::new(stdout.lock());
    let charset: ddddocr::Charset = include_str!("../model/common_old.json")
        .parse()
        .context("parse charset")?;
    let ocr = Ddddocr::new(include_bytes!("../model/common_old.onnx"), charset)
        .context("load ocr model")?;
    let det =
        Ddddocr::new_model(include_bytes!("../model/common_det.onnx")).context("load det model")?;
    emit(&mut out, serde_json::json!({"ready": true}))?;

    let stdin = std::io::stdin();
    for line in stdin.lock().lines() {
        let line = line.context("read job")?;
        if line.trim().is_empty() {
            continue;
        }
        let reply = match solve(&ocr, &det, &line) {
            Ok(points) => serde_json::json!({"points": points}),
            Err(err) => serde_json::json!({"error": one_line(&err)}),
        };
        emit(&mut out, reply)?;
    }
    Ok(())
}

fn emit(out: &mut impl Write, value: serde_json::Value) -> Result<()> {
    serde_json::to_writer(&mut *out, &value)?;
    out.write_all(b"\n")?;
    out.flush()?;
    Ok(())
}

fn one_line(err: &anyhow::Error) -> String {
    err.to_string().replace(['\n', '\r'], " ")
}

fn solve(ocr: &Ddddocr<'_>, det: &Ddddocr<'_>, line: &str) -> Result<Vec<Point>> {
    let job: Job = serde_json::from_str(line).context("parse job")?;
    if job.words.is_empty() {
        bail!("没有给出要点的字");
    }
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(job.image.trim())
        .context("decode image")?;
    let img = image::load_from_memory(&bytes).context("open image")?;
    let (width, height) = img.dimensions();
    if width == 0 || height == 0 {
        bail!("验证码图片是空的");
    }
    let mut boxes = det.detection(&bytes).context("detect")?;
    boxes.retain(|b| area(*b) >= 16 && b.x2 > b.x1 && b.y2 > b.y1);
    boxes.sort_by_key(|b| std::cmp::Reverse(area(*b)));
    if boxes.len() > 12 {
        boxes.truncate(12);
    }
    if boxes.is_empty() {
        bail!("图上没有检出文字");
    }

    let mut hits = Vec::with_capacity(boxes.len());
    for bbox in &boxes {
        let png = crop_png(&img, *bbox, width, height)?;
        let text = ocr.classification(&png).context("ocr")?;
        let prob = ocr
            .classification_probability(&png)
            .context("ocr probability")?;
        hits.push(Hit {
            text,
            prob,
            point: to_point(*bbox, width, height),
        });
    }

    let mut pairs = Vec::new();
    for (wi, word) in job.words.iter().enumerate() {
        for (bi, hit) in hits.iter().enumerate() {
            pairs.push((word_score(hit, word), wi, bi));
        }
    }
    pairs.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap_or(std::cmp::Ordering::Equal));

    let mut taken_word = vec![false; job.words.len()];
    let mut taken_box = vec![false; hits.len()];
    let mut chosen = vec![None; job.words.len()];
    for (rank, wi, bi) in pairs {
        if rank <= 0.0 || taken_word[wi] || taken_box[bi] {
            continue;
        }
        taken_word[wi] = true;
        taken_box[bi] = true;
        chosen[wi] = Some(bi);
    }

    let mut points = Vec::with_capacity(job.words.len());
    for (wi, slot) in chosen.into_iter().enumerate() {
        let Some(bi) = slot else {
            bail!("没有对上 \"{}\"", job.words[wi]);
        };
        points.push(hits[bi].point);
    }
    Ok(points)
}

fn crop_png(img: &DynamicImage, bbox: BBox, width: u32, height: u32) -> Result<Vec<u8>> {
    let pad = 3u32;
    let x1 = bbox.x1.saturating_sub(pad);
    let y1 = bbox.y1.saturating_sub(pad);
    let x2 = (bbox.x2 + pad).min(width.saturating_sub(1));
    let y2 = (bbox.y2 + pad).min(height.saturating_sub(1));
    if x2 < x1 || y2 < y1 {
        bail!("文字框无效");
    }
    let tile = image::imageops::crop_imm(img, x1, y1, x2 - x1 + 1, y2 - y1 + 1).to_image();
    let mut buf = std::io::Cursor::new(Vec::new());
    DynamicImage::ImageRgba8(tile)
        .write_to(&mut buf, image::ImageFormat::Png)
        .context("encode crop")?;
    Ok(buf.into_inner())
}

fn to_point(bbox: BBox, width: u32, height: u32) -> Point {
    let cx = (f64::from(bbox.x1) + f64::from(bbox.x2)) / 2.0;
    let cy = (f64::from(bbox.y1) + f64::from(bbox.y2)) / 2.0;
    let x = (310.0 * cx / f64::from(width)).round() as i32;
    let y = (155.0 * cy / f64::from(height)).round() as i32;
    Point {
        x: x.clamp(0, 309),
        y: y.clamp(0, 154),
    }
}

fn area(bbox: BBox) -> u32 {
    bbox.x2.saturating_sub(bbox.x1).saturating_add(1)
        * bbox.y2.saturating_sub(bbox.y1).saturating_add(1)
}

fn word_score(hit: &Hit, word: &str) -> f32 {
    let mut score = char_score(&hit.prob, word);
    if hit.text == word {
        score += 1.0;
    } else if word.chars().count() == 1 && hit.text.contains(word) {
        score += 0.5;
    }
    score
}

fn char_score(prob: &CharacterProbability, word: &str) -> f32 {
    let Some(idx) = prob.charset.iter().position(|item| item == word) else {
        return 0.0;
    };
    let mut best = 0.0f32;
    for step in &prob.probability {
        if idx < step.len() && step[idx] > best {
            best = step[idx];
        }
    }
    best
}
