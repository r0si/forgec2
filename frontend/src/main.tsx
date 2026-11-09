import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "react-router-dom";
import "./styles/globals.css";
import ClientProvider from "@/components/ClientProvider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { router } from "./router";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TooltipProvider>
      <ClientProvider>
        <RouterProvider router={router} />
      </ClientProvider>
    </TooltipProvider>
  </StrictMode>
);
