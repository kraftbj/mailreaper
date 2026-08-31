/**
 * settings.js — settings page logic.
 */
import { api } from "./app.js";

let allCategories = [];

// ---- Categories ----

async function loadCategories() {
  allCategories = await api("/api/categories").catch(() => []);
  renderCategories();
}

function renderCategories() {
  const tbody = document.getElementById("categories-tbody");
  if (!tbody) return;

  if (allCategories.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">No categories yet.</td></tr>`;
    return;
  }

  tbody.innerHTML = allCategories.map((c) => `
    <tr>
      <td>${esc(c.icon || "")}</td>
      <td>${esc(c.name)}</td>
      <td>${esc(c.folderName || "")}</td>
      <td>
        ${c.color
          ? `<span style="display:inline-block;width:14px;height:14px;border-radius:3px;background:${esc(c.color)};vertical-align:middle;margin-right:6px"></span>${esc(c.color)}`
          : "—"}
      </td>
      <td>${c.checkDeadlines ? "Yes" : "—"}</td>
      <td>
        <div class="flex gap-8">
          <button class="btn btn-sm btn-outline" data-edit-cat="${esc(c.id)}">Edit</button>
          <button class="btn btn-sm btn-danger"  data-delete-cat="${esc(c.id)}">Delete</button>
        </div>
      </td>
    </tr>
  `).join("");
}

function openCategoryModal(cat = null) {
  const modal = document.getElementById("category-modal");
  if (!modal) return;
  document.getElementById("cat-modal-title").textContent = cat ? "Edit Category" : "Add Category";
  document.getElementById("cat-id").value = cat?.id || "";
  document.getElementById("cat-name").value = cat?.name || "";
  document.getElementById("cat-folder").value = cat?.folderName || "";
  document.getElementById("cat-icon").value = cat?.icon || "";
  document.getElementById("cat-color").value = cat?.color || "";
  document.getElementById("cat-check-deadlines").checked = Boolean(cat?.checkDeadlines);
  modal.classList.remove("hidden");
}

function closeCategoryModal() {
  document.getElementById("category-modal")?.classList.add("hidden");
}

async function saveCategory() {
  const name = document.getElementById("cat-name").value.trim();
  if (!name) { alert("Category name is required."); return; }

  const id = document.getElementById("cat-id").value || name.toLowerCase().replace(/\s+/g, "-");
  const cat = {
    id,
    name,
    folderName: document.getElementById("cat-folder").value.trim() || name,
    icon: document.getElementById("cat-icon").value.trim(),
    color: document.getElementById("cat-color").value.trim(),
    checkDeadlines: document.getElementById("cat-check-deadlines").checked,
  };

  try {
    await api("/api/categories", { method: "POST", body: JSON.stringify(cat) });
    closeCategoryModal();
    await loadCategories();
  } catch (err) {
    alert(`Failed to save category: ${err.message}`);
  }
}

async function deleteCategory(id) {
  if (!confirm("Delete this category?")) return;
  try {
    await api(`/api/categories/${encodeURIComponent(id)}`, { method: "DELETE" });
    await loadCategories();
  } catch (err) {
    alert(`Failed to delete category: ${err.message}`);
  }
}

// ---- Training Examples ----

async function loadTrainingExamples() {
  const category = document.getElementById("training-category-filter")?.value || "";
  const path = category ? `/api/training?category=${encodeURIComponent(category)}` : "/api/training";
  const examples = await api(path).catch(() => []);
  renderTrainingExamples(examples);
}

function renderTrainingExamples(examples) {
  const tbody = document.getElementById("training-tbody");
  if (!tbody) return;

  if (examples.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">No training examples.</td></tr>`;
    return;
  }

  tbody.innerHTML = examples.map((ex) => `
    <tr>
      <td>${esc(ex.category || "")}</td>
      <td style="max-width:240px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc(ex.subject)}">${esc(ex.subject || "")}</td>
      <td style="max-width:180px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc(ex.sender)}">${esc(ex.sender || "")}</td>
      <td><span class="type-badge ${ex.label === "keep" ? "type-header" : "type-ttl"}">${esc(ex.label || "")}</span></td>
      <td>${esc(ex.source || "")}</td>
      <td>
        <button class="btn btn-sm btn-danger" data-delete-example="${ex.id}">Delete</button>
      </td>
    </tr>
  `).join("");
}

async function deleteTrainingExample(id) {
  if (!confirm("Delete this training example?")) return;
  try {
    await api(`/api/training/${encodeURIComponent(id)}`, { method: "DELETE" });
    await loadTrainingExamples();
  } catch (err) {
    alert(`Failed to delete training example: ${err.message}`);
  }
}

// ---- Accounts (read-only display) ----

async function loadAccountStatus() {
  // No dedicated /api/accounts endpoint; show placeholder guidance.
  const el = document.getElementById("accounts-list");
  if (!el) return;
  el.innerHTML = `<p style="margin-top:4px">Account details are configured in <code>config.yaml</code>. Restart the service after editing.</p>`;
}

// ---- Utilities ----

function esc(str) {
  if (!str) return "";
  return String(str)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// ---- Init ----

document.addEventListener("DOMContentLoaded", () => {
  loadCategories();
  loadTrainingExamples();
  loadAccountStatus();

  document.getElementById("add-category-btn")?.addEventListener("click", () => openCategoryModal());
  document.getElementById("cat-cancel-btn")?.addEventListener("click", closeCategoryModal);
  document.getElementById("cat-save-btn")?.addEventListener("click", saveCategory);

  document.getElementById("category-modal")?.addEventListener("click", (e) => {
    if (e.target === e.currentTarget) closeCategoryModal();
  });

  document.getElementById("categories-tbody")?.addEventListener("click", async (e) => {
    const editBtn = e.target.closest("[data-edit-cat]");
    const delBtn = e.target.closest("[data-delete-cat]");
    if (editBtn) {
      const cat = allCategories.find((c) => c.id === editBtn.dataset.editCat);
      if (cat) openCategoryModal(cat);
    } else if (delBtn) {
      await deleteCategory(delBtn.dataset.deleteCat);
    }
  });

  document.getElementById("training-tbody")?.addEventListener("click", async (e) => {
    const delBtn = e.target.closest("[data-delete-example]");
    if (delBtn) await deleteTrainingExample(delBtn.dataset.deleteExample);
  });

  document.getElementById("training-category-filter")?.addEventListener("change", loadTrainingExamples);
});
