#!/usr/bin/env node
"use strict";

const PptxGenJS = require("pptxgenjs");
const fs = require("fs");
const path = require("path");

function parseArgs(args) {
  const opts = {};
  let i = 0;
  while (i < args.length) {
    const arg = args[i];
    if (arg === "--theme") {
      opts.themeFile = args[++i];
    } else if (!opts.input) {
      opts.input = arg;
    } else if (!opts.output) {
      opts.output = arg;
    }
    i++;
  }
  return opts;
}

function resolveColor(color, theme) {
  if (theme && theme[color]) return theme[color];
  if (color && color.startsWith("#")) return color.replace("#", "");
  return color || "333333";
}

function addCoverSlide(pptx, slide, theme) {
  const s = pptx.addSlide();
  s.background = { color: resolveColor("bg", theme) };

  // Decorative top bar.
  s.addShape(pptx.ShapeType.rect, {
    x: "0%", y: "0%", w: "100%", h: "3%",
    fill: { color: resolveColor("primary", theme) },
  });

  s.addText(slide.title || "", {
    x: "10%", y: "30%", w: "80%", h: "15%",
    fontSize: 36, fontFace: theme.fontFace || "Inter",
    color: resolveColor("primary", theme),
    bold: true, align: "center",
  });

  if (slide.subtitle) {
    s.addText(slide.subtitle, {
      x: "15%", y: "48%", w: "70%", h: "8%",
      fontSize: 18, fontFace: theme.fontFace || "Inter",
      color: resolveColor("secondary", theme),
      align: "center",
    });
  }

  if (slide.date) {
    s.addText(slide.date, {
      x: "15%", y: "70%", w: "70%", h: "5%",
      fontSize: 12, fontFace: theme.fontFace || "Inter",
      color: resolveColor("muted", theme) || "999999",
      align: "center",
    });
  }
}

function addTocSlide(pptx, slide, theme) {
  const s = pptx.addSlide();
  s.background = { color: resolveColor("bg", theme) };

  s.addText(slide.title || "Agenda", {
    x: "5%", y: "5%", w: "90%", h: "10%",
    fontSize: 28, bold: true,
    color: resolveColor("primary", theme),
    fontFace: theme.fontFace || "Inter",
  });

  const items = slide.items || [];
  items.forEach((item, idx) => {
    const yPos = 20 + idx * 10;
    s.addText(`${idx + 1}.  ${item}`, {
      x: "10%", y: `${yPos}%`, w: "80%", h: "8%",
      fontSize: 18, color: resolveColor("secondary", theme),
      fontFace: theme.fontFace || "Inter",
    });
  });
}

function addSectionSlide(pptx, slide, theme) {
  const s = pptx.addSlide();
  s.background = { color: resolveColor("light", theme) || "F5F5F5" };

  s.addText(slide.title || "", {
    x: "10%", y: "35%", w: "80%", h: "15%",
    fontSize: 32, bold: true,
    color: resolveColor("primary", theme),
    fontFace: theme.fontFace || "Inter",
    align: "center",
  });

  if (slide.subtitle) {
    s.addText(slide.subtitle, {
      x: "15%", y: "52%", w: "70%", h: "10%",
      fontSize: 16, color: resolveColor("secondary", theme),
      fontFace: theme.fontFace || "Inter",
      align: "center",
    });
  }
}

function addContentSlide(pptx, slide, theme) {
  const s = pptx.addSlide();
  s.background = { color: resolveColor("bg", theme) };

  // Title bar.
  s.addText(slide.title || "", {
    x: "5%", y: "3%", w: "90%", h: "8%",
    fontSize: 22, bold: true,
    color: resolveColor("primary", theme),
    fontFace: theme.fontFace || "Inter",
  });

  // Accent line under title.
  s.addShape(pptx.ShapeType.rect, {
    x: "5%", y: "12%", w: "20%", h: "0.5%",
    fill: { color: resolveColor("accent", theme) },
  });

  // Elements.
  for (const el of slide.elements || []) {
    addElement(s, pptx, el, theme);
  }
}

function addSummarySlide(pptx, slide, theme) {
  const s = pptx.addSlide();
  s.background = { color: resolveColor("bg", theme) };

  s.addText(slide.title || "Key Takeaways", {
    x: "5%", y: "5%", w: "90%", h: "10%",
    fontSize: 28, bold: true,
    color: resolveColor("primary", theme),
    fontFace: theme.fontFace || "Inter",
  });

  const bullets = slide.bullets || [];
  const bodyText = bullets.map((b) => ({
    text: b,
    options: {
      fontSize: 18,
      color: resolveColor("text", theme) || "333333",
      fontFace: theme.fontFace || "Inter",
      bullet: { code: "2022" },
      breakLine: true,
      paraSpaceAfter: 14,
    },
  }));

  s.addText(bodyText, {
    x: "8%", y: "20%", w: "84%", h: "65%",
    valign: "top",
  });
}

function addElement(s, pptx, el, theme) {
  const pos = el.position || {};

  switch (el.type) {
    case "text":
      s.addText(el.text || "", {
        x: pos.x || "5%", y: pos.y || "15%",
        w: pos.w || "90%", h: pos.h || "auto",
        fontSize: el.fontSize || 14,
        fontFace: el.fontFace || theme.fontFace || "Inter",
        color: resolveColor(el.color || "text", theme),
        bold: el.bold || false,
        italic: el.italic || false,
        align: el.align || "left",
      });
      break;

    case "chart": {
      const chartData = [];
      const data = el.data || {};
      for (const series of data.series || []) {
        chartData.push({
          name: series.name || "",
          labels: data.labels || [],
          values: (series.values || []).map((v) => (v === null ? 0 : v)),
        });
      }

      const chartTypeMap = {
        bar: pptx.ChartType.bar,
        line: pptx.ChartType.line,
        pie: pptx.ChartType.pie,
        doughnut: pptx.ChartType.doughnut,
        area: pptx.ChartType.area,
        scatter: pptx.ChartType.scatter,
      };

      s.addChart(chartTypeMap[el.chartType] || pptx.ChartType.bar, chartData, {
        x: pos.x || "5%", y: pos.y || "15%",
        w: pos.w || "55%", h: pos.h || "70%",
        showTitle: !!el.title,
        title: el.title || "",
        showValue: el.showValue || false,
        showLegend: el.showLegend !== false,
        chartColors: [
          resolveColor("primary", theme),
          resolveColor("secondary", theme),
          resolveColor("accent", theme),
        ],
      });
      break;
    }

    case "table": {
      const tableRows = [el.headers || [], ...(el.rows || [])];
      s.addTable(tableRows, {
        x: pos.x || "5%", y: pos.y || "15%",
        w: pos.w || "90%", h: pos.h || "auto",
        fontSize: el.fontSize || 12,
        fontFace: theme.fontFace || "Inter",
        border: { type: "solid", pt: 0.5, color: "CCCCCC" },
        colW: undefined,
        autoPage: false,
        rowH: undefined,
      });
      break;
    }

    case "image":
      s.addImage({
        path: el.path,
        x: pos.x || "5%", y: pos.y || "15%",
        w: pos.w || "90%", h: pos.h || "70%",
        sizing: { type: el.sizing || "contain" },
      });
      break;

    case "shape":
      s.addShape(pptx.ShapeType[el.shapeType] || pptx.ShapeType.rect, {
        x: pos.x || "10%", y: pos.y || "10%",
        w: pos.w || "20%", h: pos.h || "20%",
        fill: { color: resolveColor(el.fill || "primary", theme) },
        shadow: el.shadow ? { type: "outer", blur: 6, offset: 2, color: "000000", opacity: 0.3 } : undefined,
      });
      if (el.text) {
        // Text inside shape is handled via shape options — but PptxGenJS shapes
        // with text need addText overlay. Simplified: add separate text.
        s.addText(el.text, {
          x: pos.x || "10%", y: pos.y || "10%",
          w: pos.w || "20%", h: pos.h || "20%",
          fontSize: el.fontSize || 14,
          color: "FFFFFF",
          align: "center", valign: "middle",
          fontFace: theme.fontFace || "Inter",
        });
      }
      break;

    case "kpi": {
      // KPI card: large value + small label.
      const kpiX = pos.x || "5%";
      const kpiY = pos.y || "15%";
      const kpiW = pos.w || "25%";
      const kpiH = pos.h || "20%";

      // Background shape.
      s.addShape(pptx.ShapeType.roundRect, {
        x: kpiX, y: kpiY, w: kpiW, h: kpiH,
        fill: { color: resolveColor("light", theme) || "F5F5F5" },
        rectRadius: 0.1,
      });

      s.addText(el.value || "", {
        x: kpiX, y: kpiY, w: kpiW, h: `${parseInt(kpiH) * 0.6}%`,
        fontSize: el.valueSize || 36, bold: true,
        color: resolveColor(el.color || "primary", theme),
        fontFace: theme.fontFace || "Inter",
        align: "center", valign: "bottom",
      });

      s.addText(el.label || "", {
        x: kpiX, y: `${parseInt(kpiY) + parseInt(kpiH) * 0.55}%`,
        w: kpiW, h: `${parseInt(kpiH) * 0.35}%`,
        fontSize: el.labelSize || 12,
        color: resolveColor("muted", theme) || "999999",
        fontFace: theme.fontFace || "Inter",
        align: "center", valign: "top",
      });
      break;
    }
  }
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));

  if (!opts.input || !opts.output) {
    console.error("Usage: compile.js <spec.json> <output.pptx> [--theme theme.json]");
    process.exit(1);
  }

  const spec = JSON.parse(fs.readFileSync(opts.input, "utf8"));

  // Merge theme: spec.theme is base, --theme file overrides.
  let theme = spec.theme || {};
  if (opts.themeFile) {
    const override = JSON.parse(fs.readFileSync(opts.themeFile, "utf8"));
    theme = { ...theme, ...override };
  }

  const pptx = new PptxGenJS();
  pptx.layout = "LAYOUT_WIDE";
  pptx.author = "oasis-render";

  for (const slide of spec.slides || []) {
    switch (slide.layout) {
      case "cover":   addCoverSlide(pptx, slide, theme); break;
      case "toc":     addTocSlide(pptx, slide, theme); break;
      case "section": addSectionSlide(pptx, slide, theme); break;
      case "content": addContentSlide(pptx, slide, theme); break;
      case "summary": addSummarySlide(pptx, slide, theme); break;
      default:        addContentSlide(pptx, slide, theme); break;
    }
  }

  await pptx.writeFile({ fileName: opts.output });
  console.log(path.resolve(opts.output));
}

main().catch((err) => {
  console.error(err.message);
  process.exit(1);
});
