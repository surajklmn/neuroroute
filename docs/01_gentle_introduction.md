# 01: A Gentle Introduction to NeuroRoute

Welcome to **NeuroRoute**! If you are new to web development, systems architecture, or machine learning, this is the perfect place to start. This guide is written specifically to explain the core concepts of our smart Layer 7 reverse proxy load balancer using everyday language and simple analogies.

By the end of this guide, you will understand exactly why NeuroRoute exists, what problems it solves, and how it works conceptually.

---

## Core Concepts in Plain English

Before we look at code, let's understand the two foundational technologies behind this project:

### 1. What is a Reverse Proxy & Load Balancer?
In a standard web application, clients (like mobile apps or web browsers) send requests to a server to fetch or submit data. 
*   **The Problem:** If millions of clients talk to a single server, that server gets overwhelmed and crashes.
*   **The Reverse Proxy:** Think of this as a **receptionist** standing in front of an office. Instead of walking directly into the back office to talk to a developer, you talk to the receptionist.
*   **The Load Balancer:** The receptionist doesn't do the work themselves. Instead, they check which developer is currently free and hands your request to them. In software, this receptionist is called a **Layer 7 Reverse Proxy Load Balancer**.

### 2. What is Head-of-Line (HoL) Blocking?
This is the central problem NeuroRoute is designed to solve. 

> [!TIP]
> **The Grocery Store Checkout Analogy**
> Imagine you are at a grocery store. You only want to buy a single pack of gum (a **Light Request** that takes 2 seconds to pay for).
>
> However, you get in line behind a customer who has two overflowing carts of items, dynamic price matches, and a stack of expired coupons (a **Heavy Request** that takes 15 minutes to process).
>
> Even though your transaction takes 2 seconds, you are forced to wait 15 minutes because the person in front of you is blocking the single queue lane. This is **Head-of-Line (HoL) blocking**.

---

## The NeuroRoute Solution: Highway Traffic Lanes

In a traditional load balancer, all requests—regardless of whether they are fast or slow—are thrown into the same worker queue. If the system gets hammered with a few massive requests (like calculating a huge mathematical matrix or searching a massive database), the fast requests get stuck behind them, causing timeouts and high latency.

NeuroRoute solves this by building **three physically isolated lanes of traffic**, similar to a highway:

```
                            INCOMING VEHICLES (Requests)
                                         │
                        ┌────────────────▼────────────────┐
                        │      NeuroRoute tollbooth       │ 
                        │ (Inspects vehicle in microsecs) │
                        └───────┬────────┬────────┬───────┘
                                │        │        │
                         Motorcycles  Sedans   Heavy Trucks
                         (Light C0)  (Med C1)   (Heavy C2)
                                │        │        │
                                ▼        ▼        ▼
                             ┌─────┐  ┌─────┐  ┌─────┐
                             │ C0  │  │ C1  │  │ C2  │
                             │Lane │  │Lane │  │Lane │
                             └─────┘  └─────┘  └─────┘
```

*   **The Fast Lane (Class 0 - Light):** Exclusively reserved for ultra-fast, lightweight requests (like quick page loads). Restricted to tiny system resources because they execute instantly.
*   **The Medium Lane (Class 1 - Medium):** Designed for standard requests (like loading profile data or updating comments). Bounded to medium resources.
*   **The Slow Lane (Class 2 - Heavy):** The designated quarantine lane for heavy, slow, or complex requests (like image processing or large mathematical operations). Bounded to high-powered workers.

By segregating the traffic at the gate, a sudden flood of heavy truck traffic (Slow Requests) will completely congest the **Slow Lane**, but the **Fast Lane** remains wide open, allowing lightweight requests to breeze through in under a millisecond!

---

## Establish Physical Barriers: How Docker Cgroups Work

How do we prevent a heavy truck from leaking out of the Slow Lane and stealing all of the CPU power from the Fast Lane? In standard software, all programs on your machine fight for the same pool of CPU and RAM. If one program goes haywire, it slows down the entire machine.

NeuroRoute prevents this by utilizing **Docker Cgroups (Control Groups)**.

> [!NOTE]
> **Docker Cgroups** are virtual walls built around our worker programs. We tell the operating system: *"Worker 1 is only allowed to use a maximum of 25% of one CPU core, whereas Worker 4 is allowed to use 100% of a CPU core."*

### Our Worker Resource Grid

Here is how our lanes are physically locked down inside [docker-compose.yml](../docker-compose.yml):

| Lane Class | Assigned Workers | CPU Limit | Memory Limit | Target Traffic Profile |
| :--- | :--- | :--- | :--- | :--- |
| **Class 0 (Fast)** | Worker 1 & 2 | **0.25 Cores** | **128 MB** | Simple API fetches, short pings ($<10\text{ms}$) |
| **Class 1 (Medium)** | Worker 3 | **0.50 Cores** | **256 MB** | Normal queries, DB selects ($10\text{ms} - 200\text{ms}$) |
| **Class 2 (Heavy)** | Worker 4 & 5 | **1.00 Cores** | **512 MB** | Matrix multiplication, heavy queries ($>200\text{ms}$) |

If a malicious or heavy request hits Worker 4 in the Slow Lane, it can consume 100% of Worker 4's allocated CPU, but the operating system guarantees it cannot touch the CPU shares allocated to Worker 1 and 2 in the Fast Lane. **Your fast requests remain 100% responsive and safe!**

---

## How Traffic Moves: The Architecture Path

Here is a visual map showing what happens to a request from the moment you execute a `curl` command to the moment you get a response back:

```
[ Client Request ]
       │
       ▼
┌────────────────────────────────────────────────────────┐
│ NeuroRoute Gateway (:8000)                             │
├────────────────────────────────────────────────────────┤
│ 1. Peeks at request body header (reads first 512 bytes)│
│ 2. Extracts features (payload size, URL keywords)     │
│ 3. Feeds features to the Embedded ML Predictor         │
│ 4. Classifies request: LIGHT, MEDIUM, or HEAVY         │
│ 5. Selects target worker pool lane                     │
│ 6. Selects worker in lane with the least active work   │
└──────────────────────────┬─────────────────────────────┘
                           │
             ┌─────────────┼─────────────┐
             ▼             ▼             ▼
       [ Fast Lane ] [ Medium Lane ] [ Slow Lane ]
         Worker 1      Worker 3        Worker 4
         Worker 2                      Worker 5
             │             │             │
             └─────────────┼─────────────┘
                           │
                           ▼
                    [ Client Response ]
```

In the next guide, [02: Machine Learning for Non-ML Developers](02_ml_for_non_ml_devs.md), we will explore exactly how the gateway makes these classification decisions in under **10 microseconds** without using a slow external database or web service!
