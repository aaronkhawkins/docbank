import { mount } from "svelte";
import { initTheme } from "@kenn-io/kit-ui";
import "@kenn-io/kit-ui/theme.css";
import "@kenn-io/kit-ui/fonts.css";
import "./app.css";
import App from "./App.svelte";
import DocumentViewer from "./DocumentViewer.svelte";

initTheme({ storageKey: "docbank-theme" });

const target = document.getElementById("app");
if (!target) {
  throw new Error("Docbank web application root is missing");
}

const component = location.pathname.startsWith("/documents/") ? DocumentViewer : App;
mount(component, { target });
